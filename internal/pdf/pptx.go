package pdf

// PDF 转 PPT：每页渲染成一张图片铺满一页幻灯片，页面上的文字放进演讲者备注

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/haoawake/omni-convert/internal/conv"
)

const (
	emuPerPt   = 12700
	slideMin   = 914400   // 幻灯片最小 1 英寸
	slideMax   = 51206400 // 最大 56 英寸
	pptDPI     = 200
	pptMaxSide = 3000 // 图片长边最多多少像素
)

// slideSize 按第一页的比例定幻灯片大小（EMU），限制在 PowerPoint 允许的范围里
func slideSize(wPt, hPt float64) (int64, int64) {
	cx, cy := wPt*emuPerPt, hPt*emuPerPt
	if m := math.Max(cx, cy); m > slideMax {
		cx, cy = cx*slideMax/m, cy*slideMax/m
	}
	if m := math.Min(cx, cy); m < slideMin {
		cx, cy = cx*slideMin/m, cy*slideMin/m
	}
	return min(max(int64(cx), slideMin), slideMax), min(max(int64(cy), slideMin), slideMax)
}

// ToPPT 把 PDF 转成 .pptx：一页一张幻灯片，内容是整页的图片（约 200 dpi），文字放在备注里
func ToPPT() conv.RunFunc {
	return guarded(func(ctx context.Context, j *conv.Job) error {
		d, err := openForJob(j, j.Input())
		if err != nil {
			return err
		}
		defer d.Close()
		pages, err := selectedPages(j.Opt, d.PageCount())
		if err != nil {
			return err
		}
		q := jobQuality(j.Opt, 90)
		n := len(pages)
		sw, sh := d.PageSize(pages[0])
		cx, cy := slideSize(sw, sh)

		// 每页图片的像素尺寸，以及在幻灯片上的位置（等比缩放、居中）
		type place struct {
			pw, ph     int
			x, y, w, h int64
		}
		places := make([]place, n)
		for k, p := range pages {
			w, h := d.PageSize(p)
			s := float64(pptDPI) / 72
			if l := math.Max(w, h) * s; l > pptMaxSide {
				s *= pptMaxSide / l
			}
			pw, ph := clampPixels(pixelSize(w, h, s))
			fit := math.Min(float64(cx)/w, float64(cy)/h)
			iw, ih := int64(w*fit), int64(h*fit)
			places[k] = place{pw, ph, (cx - iw) / 2, (cy - ih) / 2, iw, ih}
		}
		notes := make([]string, n)

		out := j.OutFile(".pptx")
		return writeFile(out, func(w io.Writer) error {
			z := zip.NewWriter(w)
			hasNotes := func(k int) bool { return strings.TrimSpace(notes[k]) != "" }
			render := func(k, p int) (*image.RGBA, error) {
				notes[k] = d.Text(p)
				return d.RenderPx(p, places[k].pw, places[k].ph)
			}
			encode := func(k int, img *image.RGBA) (any, error) {
				var b bytes.Buffer
				err := jpeg.Encode(&b, img, &jpeg.Options{Quality: q})
				return b.Bytes(), err
			}
			emit := func(k int, v any) error {
				fw, err := z.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("ppt/media/image%d.jpg", k+1), Method: zip.Store, Modified: time.Now()})
				if err != nil {
					return err
				}
				_, err = fw.Write(v.([]byte))
				return err
			}
			if err := pipeline(ctx, j, pages, render, encode, emit, workersFor(pptMaxSide*pptMaxSide)); err != nil {
				return err
			}

			files := map[string]string{}
			var ct, sldIds, presRels strings.Builder
			for k := range pages {
				i := k + 1
				pl := places[k]
				slide := fmt.Sprintf(`<p:sld %s><p:cSld><p:spTree>%s`+
					`<p:pic><p:nvPicPr><p:cNvPr id="2" name="图片 1" descr="第 %d 页"/><p:cNvPicPr><a:picLocks noChangeAspect="1"/></p:cNvPicPr><p:nvPr/></p:nvPicPr>`+
					`<p:blipFill><a:blip r:embed="rId2"/><a:stretch><a:fillRect/></a:stretch></p:blipFill>`+
					`<p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr></p:pic>`+
					`</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sld>`,
					pmlNS, grpSpPr, pages[k]+1, pl.x, pl.y, pl.w, pl.h)
				files[fmt.Sprintf("ppt/slides/slide%d.xml", i)] = xmlHead + slide
				rels := rel("rId1", relSlideLayout, "../slideLayouts/slideLayout1.xml") +
					rel("rId2", relImage, fmt.Sprintf("../media/image%d.jpg", i))
				if hasNotes(k) {
					rels += rel("rId3", relNotesSlide, fmt.Sprintf("../notesSlides/notesSlide%d.xml", i))
					files[fmt.Sprintf("ppt/notesSlides/notesSlide%d.xml", i)] = notesSlideXML(notes[k])
					files[fmt.Sprintf("ppt/notesSlides/_rels/notesSlide%d.xml.rels", i)] = relsXML(
						rel("rId1", relNotesMaster, "../notesMasters/notesMaster1.xml") +
							rel("rId2", relSlide, fmt.Sprintf("../slides/slide%d.xml", i)))
					fmt.Fprintf(&ct, `<Override PartName="/ppt/notesSlides/notesSlide%d.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.notesSlide+xml"/>`, i)
				}
				files[fmt.Sprintf("ppt/slides/_rels/slide%d.xml.rels", i)] = relsXML(rels)
				fmt.Fprintf(&ct, `<Override PartName="/ppt/slides/slide%d.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/>`, i)
				fmt.Fprintf(&sldIds, `<p:sldId id="%d" r:id="rId%d"/>`, 255+i, 100+i)
				presRels.WriteString(rel(fmt.Sprintf("rId%d", 100+i), relSlide, fmt.Sprintf("slides/slide%d.xml", i)))
			}

			files["[Content_Types].xml"] = xmlHead + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
				`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
				`<Default Extension="xml" ContentType="application/xml"/>` +
				`<Default Extension="jpg" ContentType="image/jpeg"/>` +
				`<Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>` +
				`<Override PartName="/ppt/slideMasters/slideMaster1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideMaster+xml"/>` +
				`<Override PartName="/ppt/slideLayouts/slideLayout1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slideLayout+xml"/>` +
				`<Override PartName="/ppt/notesMasters/notesMaster1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.notesMaster+xml"/>` +
				`<Override PartName="/ppt/theme/theme1.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>` +
				`<Override PartName="/ppt/theme/theme2.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>` +
				`<Override PartName="/ppt/presProps.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presProps+xml"/>` +
				`<Override PartName="/ppt/viewProps.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.viewProps+xml"/>` +
				`<Override PartName="/ppt/tableStyles.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.tableStyles+xml"/>` +
				`<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>` +
				`<Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>` +
				ct.String() + `</Types>`
			files["_rels/.rels"] = relsXML(
				rel("rId1", relOfficeDoc, "ppt/presentation.xml") +
					rel("rId2", relCore, "docProps/core.xml") +
					rel("rId3", relApp, "docProps/app.xml"))
			files["docProps/core.xml"] = coreXML(j.Base())
			files["docProps/app.xml"] = appXML(fmt.Sprintf("<Slides>%d</Slides>", n))
			files["ppt/presentation.xml"] = xmlHead + fmt.Sprintf(`<p:presentation %s saveSubsetFonts="1">`+
				`<p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="rId1"/></p:sldMasterIdLst>`+
				`<p:notesMasterIdLst><p:notesMasterId r:id="rId2"/></p:notesMasterIdLst>`+
				`<p:sldIdLst>%s</p:sldIdLst><p:sldSz cx="%d" cy="%d"/><p:notesSz cx="6858000" cy="9144000"/>`+
				`<p:defaultTextStyle><a:defPPr><a:defRPr lang="zh-CN"/></a:defPPr></p:defaultTextStyle></p:presentation>`,
				pmlNS, sldIds.String(), cx, cy)
			files["ppt/_rels/presentation.xml.rels"] = relsXML(
				rel("rId1", relSlideMaster, "slideMasters/slideMaster1.xml") +
					rel("rId2", relNotesMaster, "notesMasters/notesMaster1.xml") +
					rel("rId3", relTheme, "theme/theme1.xml") +
					rel("rId4", relPresProps, "presProps.xml") +
					rel("rId5", relViewProps, "viewProps.xml") +
					rel("rId6", relTableStyles, "tableStyles.xml") +
					presRels.String())
			files["ppt/slideMasters/slideMaster1.xml"] = slideMasterXML
			files["ppt/slideMasters/_rels/slideMaster1.xml.rels"] = relsXML(
				rel("rId1", relSlideLayout, "../slideLayouts/slideLayout1.xml") +
					rel("rId2", relTheme, "../theme/theme1.xml"))
			files["ppt/slideLayouts/slideLayout1.xml"] = slideLayoutXML
			files["ppt/slideLayouts/_rels/slideLayout1.xml.rels"] = relsXML(rel("rId1", relSlideMaster, "../slideMasters/slideMaster1.xml"))
			files["ppt/notesMasters/notesMaster1.xml"] = notesMasterXML
			files["ppt/notesMasters/_rels/notesMaster1.xml.rels"] = relsXML(rel("rId1", relTheme, "../theme/theme2.xml"))
			files["ppt/theme/theme1.xml"] = themeXML
			files["ppt/theme/theme2.xml"] = themeXML
			files["ppt/presProps.xml"] = xmlHead + `<p:presentationPr ` + pmlNS + `/>`
			files["ppt/viewProps.xml"] = xmlHead + `<p:viewPr ` + pmlNS + `><p:normalViewPr><p:restoredLeft sz="15620"/><p:restoredTop sz="94660"/></p:normalViewPr><p:gridSpacing cx="72008" cy="72008"/></p:viewPr>`
			files["ppt/tableStyles.xml"] = xmlHead + `<a:tblStyleLst xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" def="{5C22544A-7EE6-4342-B048-85BDC9FD1C3A}"/>`

			names := sortedKeys(files)
			for _, name := range names {
				if err := zipText(z, name, files[name]); err != nil {
					return err
				}
			}
			return z.Close()
		})
	})
}

// sortedKeys 按名字排好序，[Content_Types].xml 放最前面
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	const ct = "[Content_Types].xml"
	sort.Slice(keys, func(a, b int) bool {
		if (keys[a] == ct) != (keys[b] == ct) {
			return keys[a] == ct
		}
		return keys[a] < keys[b]
	})
	return keys
}

// notesSlideXML 生成一页备注，每行文字一段
func notesSlideXML(text string) string {
	var paras strings.Builder
	for _, l := range strings.Split(text, "\n") {
		paras.WriteString("<a:p>")
		if l != "" {
			paras.WriteString(`<a:r><a:rPr lang="zh-CN" altLang="en-US" dirty="0"/><a:t>`)
			paras.WriteString(xmlEscape(strings.ReplaceAll(l, "\t", " ")))
			paras.WriteString(`</a:t></a:r>`)
		}
		paras.WriteString("</a:p>")
	}
	return xmlHead + `<p:notes ` + pmlNS + `><p:cSld><p:spTree>` + grpSpPr +
		`<p:sp><p:nvSpPr><p:cNvPr id="2" name="幻灯片图像占位符 1"/><p:cNvSpPr><a:spLocks noGrp="1" noRot="1" noChangeAspect="1"/></p:cNvSpPr><p:nvPr><p:ph type="sldImg"/></p:nvPr></p:nvSpPr><p:spPr/></p:sp>` +
		`<p:sp><p:nvSpPr><p:cNvPr id="3" name="备注占位符 2"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph type="body" idx="1"/></p:nvPr></p:nvSpPr><p:spPr/>` +
		`<p:txBody><a:bodyPr/><a:lstStyle/>` + paras.String() + `</p:txBody></p:sp>` +
		`</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:notes>`
}

// ---------------------------------------------------------------- 固定的部件

const pmlNS = `xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"`

const grpSpPr = `<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr>` +
	`<p:grpSpPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/><a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr>`

const (
	relBase        = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/"
	relOfficeDoc   = relBase + "officeDocument"
	relCore        = "http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties"
	relApp         = relBase + "extended-properties"
	relSlide       = relBase + "slide"
	relSlideLayout = relBase + "slideLayout"
	relSlideMaster = relBase + "slideMaster"
	relNotesSlide  = relBase + "notesSlide"
	relNotesMaster = relBase + "notesMaster"
	relTheme       = relBase + "theme"
	relImage       = relBase + "image"
	relPresProps   = relBase + "presProps"
	relViewProps   = relBase + "viewProps"
	relTableStyles = relBase + "tableStyles"
)

func rel(id, typ, target string) string {
	return `<Relationship Id="` + id + `" Type="` + typ + `" Target="` + target + `"/>`
}

func relsXML(rels string) string {
	return xmlHead + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` + rels + `</Relationships>`
}

const clrMap = `<p:clrMap bg1="lt1" tx1="dk1" bg2="lt2" tx2="dk2" accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" hlink="hlink" folHlink="folHlink"/>`

const defRPr = `<a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mn-lt"/><a:ea typeface="+mn-ea"/><a:cs typeface="+mn-cs"/>`

const slideMasterXML = xmlHead + `<p:sldMaster ` + pmlNS + `><p:cSld><p:bg><p:bgRef idx="1001"><a:schemeClr val="bg1"/></p:bgRef></p:bg><p:spTree>` + grpSpPr + `</p:spTree></p:cSld>` +
	clrMap +
	`<p:sldLayoutIdLst><p:sldLayoutId id="2147483649" r:id="rId1"/></p:sldLayoutIdLst>` +
	`<p:txStyles>` +
	`<p:titleStyle><a:lvl1pPr algn="l" defTabSz="914400" rtl="0" eaLnBrk="1" latinLnBrk="0" hangingPunct="1"><a:defRPr sz="4400" kern="1200"><a:solidFill><a:schemeClr val="tx1"/></a:solidFill><a:latin typeface="+mj-lt"/><a:ea typeface="+mj-ea"/><a:cs typeface="+mj-cs"/></a:defRPr></a:lvl1pPr></p:titleStyle>` +
	`<p:bodyStyle><a:lvl1pPr marL="0" indent="0" algn="l" defTabSz="914400" rtl="0" eaLnBrk="1" latinLnBrk="0" hangingPunct="1"><a:defRPr sz="2800" kern="1200">` + defRPr + `</a:defRPr></a:lvl1pPr></p:bodyStyle>` +
	`<p:otherStyle><a:defPPr><a:defRPr lang="zh-CN"/></a:defPPr><a:lvl1pPr marL="0" algn="l" defTabSz="914400" rtl="0" eaLnBrk="1" latinLnBrk="0" hangingPunct="1"><a:defRPr sz="1800" kern="1200">` + defRPr + `</a:defRPr></a:lvl1pPr></p:otherStyle>` +
	`</p:txStyles></p:sldMaster>`

const slideLayoutXML = xmlHead + `<p:sldLayout ` + pmlNS + ` type="blank" preserve="1"><p:cSld name="空白"><p:spTree>` + grpSpPr + `</p:spTree></p:cSld>` +
	`<p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sldLayout>`

const notesMasterXML = xmlHead + `<p:notesMaster ` + pmlNS + `><p:cSld><p:bg><p:bgRef idx="1001"><a:schemeClr val="bg1"/></p:bgRef></p:bg><p:spTree>` + grpSpPr +
	`<p:sp><p:nvSpPr><p:cNvPr id="2" name="幻灯片图像占位符 1"/><p:cNvSpPr><a:spLocks noGrp="1" noRot="1" noChangeAspect="1"/></p:cNvSpPr><p:nvPr><p:ph type="sldImg" idx="2"/></p:nvPr></p:nvSpPr>` +
	`<p:spPr><a:xfrm><a:off x="685800" y="1143000"/><a:ext cx="5486400" cy="3086100"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom><a:noFill/><a:ln w="12700"><a:solidFill><a:prstClr val="black"/></a:solidFill></a:ln></p:spPr></p:sp>` +
	`<p:sp><p:nvSpPr><p:cNvPr id="3" name="备注占位符 2"/><p:cNvSpPr><a:spLocks noGrp="1"/></p:cNvSpPr><p:nvPr><p:ph type="body" sz="quarter" idx="3"/></p:nvPr></p:nvSpPr>` +
	`<p:spPr><a:xfrm><a:off x="685800" y="4400550"/><a:ext cx="5486400" cy="3600450"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr>` +
	`<p:txBody><a:bodyPr vert="horz" lIns="91440" tIns="45720" rIns="91440" bIns="45720" rtlCol="0"/><a:lstStyle/><a:p><a:pPr lvl="0"/><a:endParaRPr lang="zh-CN"/></a:p></p:txBody></p:sp>` +
	`</p:spTree></p:cSld>` + clrMap +
	`<p:notesStyle><a:lvl1pPr marL="0" algn="l" defTabSz="914400" rtl="0" eaLnBrk="1" latinLnBrk="0" hangingPunct="1"><a:defRPr sz="1200" kern="1200">` + defRPr + `</a:defRPr></a:lvl1pPr></p:notesStyle>` +
	`</p:notesMaster>`

const themeXML = xmlHead + `<a:theme xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" name="Office 主题"><a:themeElements>` +
	`<a:clrScheme name="Office"><a:dk1><a:sysClr val="windowText" lastClr="000000"/></a:dk1><a:lt1><a:sysClr val="window" lastClr="FFFFFF"/></a:lt1>` +
	`<a:dk2><a:srgbClr val="44546A"/></a:dk2><a:lt2><a:srgbClr val="E7E6E6"/></a:lt2><a:accent1><a:srgbClr val="4472C4"/></a:accent1><a:accent2><a:srgbClr val="ED7D31"/></a:accent2>` +
	`<a:accent3><a:srgbClr val="A5A5A5"/></a:accent3><a:accent4><a:srgbClr val="FFC000"/></a:accent4><a:accent5><a:srgbClr val="5B9BD5"/></a:accent5><a:accent6><a:srgbClr val="70AD47"/></a:accent6>` +
	`<a:hlink><a:srgbClr val="0563C1"/></a:hlink><a:folHlink><a:srgbClr val="954F72"/></a:folHlink></a:clrScheme>` +
	`<a:fontScheme name="Office"><a:majorFont><a:latin typeface="Calibri Light"/><a:ea typeface=""/><a:cs typeface=""/><a:font script="Hans" typeface="等线 Light"/></a:majorFont>` +
	`<a:minorFont><a:latin typeface="Calibri"/><a:ea typeface=""/><a:cs typeface=""/><a:font script="Hans" typeface="等线"/></a:minorFont></a:fontScheme>` +
	`<a:fmtScheme name="Office"><a:fillStyleLst><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:fillStyleLst>` +
	`<a:lnStyleLst><a:ln w="6350" cap="flat" cmpd="sng" algn="ctr"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:prstDash val="solid"/><a:miter lim="800000"/></a:ln>` +
	`<a:ln w="12700" cap="flat" cmpd="sng" algn="ctr"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:prstDash val="solid"/><a:miter lim="800000"/></a:ln>` +
	`<a:ln w="19050" cap="flat" cmpd="sng" algn="ctr"><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:prstDash val="solid"/><a:miter lim="800000"/></a:ln></a:lnStyleLst>` +
	`<a:effectStyleLst><a:effectStyle><a:effectLst/></a:effectStyle><a:effectStyle><a:effectLst/></a:effectStyle><a:effectStyle><a:effectLst/></a:effectStyle></a:effectStyleLst>` +
	`<a:bgFillStyleLst><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:solidFill><a:schemeClr val="phClr"/></a:solidFill><a:solidFill><a:schemeClr val="phClr"/></a:solidFill></a:bgFillStyleLst></a:fmtScheme>` +
	`</a:themeElements><a:objectDefaults/><a:extraClrSchemeLst/></a:theme>`
