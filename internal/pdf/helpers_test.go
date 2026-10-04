package pdf

import (
	"context"
	"encoding/xml"
	"io"
	"path"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"

	"github.com/haoawake/omni-convert/internal/tools"
)

func magickPath() string { return tools.Magick() }

func runMagick(dir string, args ...string) error {
	_, err := tools.Run(context.Background(), tools.Magick(), args, &tools.RunOptions{Dir: dir})
	return err
}

// validateStrict 用 pdfcpu 的严格模式检查 PDF 结构
func validateStrict(t *testing.T, path string) {
	t.Helper()
	conf := model.NewStatelessConfiguration()
	conf.ValidationMode = model.ValidationStrict
	if err := api.ValidateFile(context.Background(), path, conf, nil); err != nil {
		t.Fatalf("严格校验没通过：%v", err)
	}
}

// checkOOXML 检查 Office 文件包的结构：每个 XML 部件都是合法 XML，每个关系的目标都存在，每个部件都有内容类型
func checkOOXML(t *testing.T, files map[string]string) {
	t.Helper()
	ct := files["[Content_Types].xml"]
	if ct == "" {
		t.Fatal("缺少 [Content_Types].xml")
	}
	for name, data := range files {
		if strings.HasSuffix(name, ".xml") || strings.HasSuffix(name, ".rels") {
			d := xml.NewDecoder(strings.NewReader(data))
			for {
				_, err := d.Token()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("%s 不是合法的 XML：%v", name, err)
				}
			}
		}
		if name == "[Content_Types].xml" {
			continue
		}
		ext := path.Ext(name)
		if !strings.Contains(ct, `PartName="/`+name+`"`) && !strings.Contains(ct, `Extension="`+strings.TrimPrefix(ext, ".")+`"`) {
			t.Fatalf("%s 没有内容类型", name)
		}
		if strings.HasSuffix(name, ".rels") {
			dir := path.Dir(path.Dir(name)) // xxx/_rels/a.xml.rels → xxx
			var rels struct {
				R []struct {
					ID     string `xml:"Id,attr"`
					Target string `xml:"Target,attr"`
				} `xml:"Relationship"`
			}
			if err := xml.Unmarshal([]byte(data), &rels); err != nil {
				t.Fatal(err)
			}
			seen := map[string]bool{}
			for _, r := range rels.R {
				if seen[r.ID] {
					t.Fatalf("%s 里的 %s 重复", name, r.ID)
				}
				seen[r.ID] = true
				target := path.Clean(path.Join(dir, r.Target))
				if dir == "." {
					target = path.Clean(r.Target)
				}
				if _, ok := files[target]; !ok {
					t.Fatalf("%s 指向的 %s 不存在", name, target)
				}
			}
		}
	}
	// 每个 Override 的部件都存在
	for _, part := range strings.Split(ct, `PartName="/`)[1:] {
		p := part[:strings.Index(part, `"`)]
		if _, ok := files[p]; !ok {
			t.Fatalf("内容类型里登记的 %s 不存在", p)
		}
	}
}
