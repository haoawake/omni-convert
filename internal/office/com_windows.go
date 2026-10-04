//go:build windows

package office

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	ole "github.com/go-ole/go-ole"
)

// IDispatch 调用的底层封装。go-ole 自带的 CallMethod 没法传「省略的可选参数」，
// 也拿不到 Office 抛出的真实错误码，所以这里自己调 Invoke。

// missing 表示省略这个可选参数（VT_ERROR + DISP_E_PARAMNOTFOUND）
type missingArg struct{}

var missing = missingArg{}

const (
	dispatchMethod  = 1
	dispatchGet     = 2
	dispatchPut     = 4
	dispidPropPut   = -3
	hrParamNotFound = 0x80020004
	hrException     = 0x80020009
	hrUnknownName   = 0x80020006
)

type dispParams struct {
	rgvarg            uintptr
	rgdispidNamedArgs uintptr
	cArgs             uint32
	cNamedArgs        uint32
}

type excepInfo struct {
	wCode             uint16
	wReserved         uint16
	bstrSource        *uint16
	bstrDescription   *uint16
	bstrHelpFile      *uint16
	dwHelpContext     uint32
	pvReserved        uintptr
	pfnDeferredFillIn uintptr
	scode             int32
}

// comError 是一次 COM 调用失败
type comError struct {
	Method string
	HR     uint32 // Invoke 的返回值
	SCode  uint32 // DISP_E_EXCEPTION 时 Office 给的错误码
	WCode  uint16
	Desc   string
}

func (e *comError) Error() string {
	code := e.Code()
	if e.Desc != "" {
		return fmt.Sprintf("%s：%s（0x%08X）", e.Method, e.Desc, code)
	}
	return fmt.Sprintf("%s 失败（0x%08X）", e.Method, code)
}

// Code 是最能说明问题的错误码
func (e *comError) Code() uint32 {
	if e.HR == hrException {
		if e.SCode != 0 {
			return e.SCode
		}
		if e.WCode != 0 {
			return 0x800A0000 | uint32(e.WCode)
		}
	}
	return e.HR
}

func asCOMError(err error) (*comError, bool) {
	var ce *comError
	ok := errors.As(err, &ce)
	return ce, ok
}

// isBusy：对方正忙（比如正在显示对话框或者在处理别的调用），过一会再试就好
func isBusy(err error) bool {
	ce, ok := asCOMError(err)
	if !ok {
		return false
	}
	switch ce.Code() {
	case 0x80010001, // RPC_E_CALL_REJECTED
		0x8001010A, // RPC_E_SERVERCALL_RETRYLATER
		0x800AC472: // VBA_E_IGNORE（Excel 正忙）
		return true
	}
	return false
}

// isDead：Office 进程已经没了（被结束、崩溃）
func isDead(err error) bool {
	ce, ok := asCOMError(err)
	if !ok {
		return false
	}
	switch ce.HR {
	case 0x800706BA, // RPC_S_SERVER_UNAVAILABLE
		0x800706BE, // RPC_S_CALL_FAILED
		0x800706BF, // RPC_S_CALL_FAILED_DNE
		0x80010108, // RPC_E_DISCONNECTED
		0x80010007, // RPC_E_SERVER_DIED
		0x80010012, // RPC_E_SERVER_DIED_DNE
		0x800401FD, // CO_E_OBJNOTCONNECTED
		0x80010114: // RPC_E_INVALID_OBJECT
		return true
	}
	return false
}

func bstrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	n := ole.SysStringLen((*int16)(unsafe.Pointer(p)))
	return syscall.UTF16ToString(unsafe.Slice(p, n))
}

func freeBSTR(p *uint16) {
	if p != nil {
		ole.SysFreeString((*int16)(unsafe.Pointer(p)))
	}
}

// invoke 调用一次 IDispatch::Invoke。args 按顺序（第一个参数在前）。
func invoke(d *ole.IDispatch, name string, flags uint16, args []any) (*ole.VARIANT, error) {
	if d == nil {
		return nil, &comError{Method: name, HR: 0x80004003} // E_POINTER
	}
	id, err := d.GetSingleIDOfName(name)
	if err != nil {
		hr := uint32(hrUnknownName)
		if oe, ok := err.(*ole.OleError); ok {
			hr = uint32(oe.Code())
		}
		return nil, &comError{Method: name, HR: hr}
	}
	n := len(args)
	vargs := make([]ole.VARIANT, n)
	var bstrs []*int16
	for i, a := range args {
		v := &vargs[n-1-i] // DISPPARAMS 里参数是倒序的
		switch x := a.(type) {
		case missingArg:
			*v = ole.VARIANT{VT: ole.VT_ERROR, Val: hrParamNotFound}
		case bool:
			*v = ole.VARIANT{VT: ole.VT_BOOL}
			if x {
				v.Val = 0xffff
			}
		case int:
			*v = ole.VARIANT{VT: ole.VT_I4, Val: int64(int32(x)) & 0xffffffff}
		case int32:
			*v = ole.VARIANT{VT: ole.VT_I4, Val: int64(x) & 0xffffffff}
		case float64:
			*v = ole.VARIANT{VT: ole.VT_R8, Val: int64(math.Float64bits(x))}
		case string:
			b := ole.SysAllocStringLen(x)
			bstrs = append(bstrs, b)
			*v = ole.VARIANT{VT: ole.VT_BSTR, Val: int64(uintptr(unsafe.Pointer(b)))}
		case *ole.IDispatch:
			*v = ole.VARIANT{VT: ole.VT_DISPATCH, Val: int64(uintptr(unsafe.Pointer(x)))}
		case obj:
			*v = ole.VARIANT{VT: ole.VT_DISPATCH, Val: int64(uintptr(unsafe.Pointer(x.d)))}
		case nil:
			*v = ole.VARIANT{VT: ole.VT_DISPATCH} // Nothing
		default:
			panic(fmt.Sprintf("office: 不支持的参数类型 %T", a))
		}
	}
	var dp dispParams
	if n > 0 {
		dp.rgvarg = uintptr(unsafe.Pointer(&vargs[0]))
		dp.cArgs = uint32(n)
	}
	named := int32(dispidPropPut)
	if flags&dispatchPut != 0 {
		dp.rgdispidNamedArgs = uintptr(unsafe.Pointer(&named))
		dp.cNamedArgs = 1
	}
	result := new(ole.VARIANT)
	ole.VariantInit(result)
	var ei excepInfo
	hr, _, _ := syscall.SyscallN(d.VTable().Invoke,
		uintptr(unsafe.Pointer(d)),
		uintptr(id),
		uintptr(unsafe.Pointer(ole.IID_NULL)),
		uintptr(ole.GetUserDefaultLCID()),
		uintptr(flags),
		uintptr(unsafe.Pointer(&dp)),
		uintptr(unsafe.Pointer(result)),
		uintptr(unsafe.Pointer(&ei)),
		0)
	runtime.KeepAlive(vargs)
	runtime.KeepAlive(&named)
	for _, b := range bstrs {
		ole.SysFreeString(b)
	}
	if hr != 0 {
		ce := &comError{Method: name, HR: uint32(hr)}
		if uint32(hr) == hrException {
			if ei.pfnDeferredFillIn != 0 {
				syscall.SyscallN(ei.pfnDeferredFillIn, uintptr(unsafe.Pointer(&ei)))
			}
			ce.SCode = uint32(ei.scode)
			ce.WCode = ei.wCode
			ce.Desc = strings.TrimSpace(bstrToString(ei.bstrDescription))
			freeBSTR(ei.bstrSource)
			freeBSTR(ei.bstrDescription)
			freeBSTR(ei.bstrHelpFile)
		}
		return nil, ce
	}
	return result, nil
}

// invokeRetry 遇到「对方正忙」时等一会儿重试（最多 20 秒）
func invokeRetry(d *ole.IDispatch, name string, flags uint16, args []any) (*ole.VARIANT, error) {
	deadline := time.Now().Add(20 * time.Second)
	for {
		v, err := invoke(d, name, flags, args)
		if err == nil || !isBusy(err) || time.Now().After(deadline) {
			return v, err
		}
		pumpMessages()
		time.Sleep(200 * time.Millisecond)
	}
}

// obj 是一个 COM 对象（IDispatch）。零值表示 Nothing。
type obj struct{ d *ole.IDispatch }

func (o obj) ok() bool { return o.d != nil }

func (o obj) release() {
	if o.d != nil {
		o.d.Release()
	}
}

// get 调用方法或读属性，返回原始结果（调用者负责 Clear）
func (o obj) get(name string, args ...any) (*ole.VARIANT, error) {
	return invokeRetry(o.d, name, dispatchMethod|dispatchGet, args)
}

// call 调用方法，丢掉返回值
func (o obj) call(name string, args ...any) error {
	v, err := o.get(name, args...)
	if v != nil {
		v.Clear()
	}
	return err
}

// put 设置属性
func (o obj) put(name string, val any) error {
	v, err := invokeRetry(o.d, name, dispatchPut, []any{val})
	if v != nil {
		v.Clear()
	}
	return err
}

// sub 取一个对象类型的属性或方法返回值。结果是 Nothing 时返回零值 obj 和 nil 错误。
func (o obj) sub(name string, args ...any) (obj, error) {
	v, err := o.get(name, args...)
	if err != nil {
		return obj{}, err
	}
	if v.VT == ole.VT_DISPATCH {
		return obj{v.ToIDispatch()}, nil // 引用转交给 obj，不 Clear
	}
	v.Clear()
	return obj{}, nil
}

// mustSub 同 sub，但结果是 Nothing 时也算错误
func (o obj) mustSub(name string, args ...any) (obj, error) {
	s, err := o.sub(name, args...)
	if err == nil && !s.ok() {
		err = &comError{Method: name, HR: 0x80004003}
	}
	return s, err
}

func (o obj) int(name string, args ...any) (int, error) {
	v, err := o.get(name, args...)
	if err != nil {
		return 0, err
	}
	defer v.Clear()
	return variantInt(v), nil
}

func (o obj) str(name string, args ...any) (string, error) {
	v, err := o.get(name, args...)
	if err != nil {
		return "", err
	}
	defer v.Clear()
	switch v.VT {
	case ole.VT_BSTR:
		return v.ToString(), nil
	case ole.VT_EMPTY, ole.VT_NULL:
		return "", nil
	}
	return fmt.Sprint(v.Value()), nil
}

func (o obj) float(name string, args ...any) (float64, error) {
	v, err := o.get(name, args...)
	if err != nil {
		return 0, err
	}
	defer v.Clear()
	switch x := v.Value().(type) {
	case float64:
		return x, nil
	case float32:
		return float64(x), nil
	}
	return float64(variantInt(v)), nil
}

func variantInt(v *ole.VARIANT) int {
	switch x := v.Value().(type) {
	case int8:
		return int(x)
	case uint8:
		return int(x)
	case int16:
		return int(x)
	case uint16:
		return int(x)
	case int32:
		return int(x)
	case uint32:
		return int(x)
	case int64:
		return int(x)
	case uint64:
		return int(x)
	case int:
		return x
	case uint:
		return int(x)
	case uintptr:
		return int(x)
	case float32:
		return int(x)
	case float64:
		return int(x)
	case bool:
		if x {
			return -1
		}
		return 0
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(x))
		return i
	}
	return 0
}

// isBoolFalse 判断属性值是不是布尔 False（Excel 的 PageSetup.Zoom 用 False 表示「按页数缩放」）
func (o obj) isBoolFalse(name string) bool {
	v, err := o.get(name)
	if err != nil {
		return false
	}
	defer v.Clear()
	return v.VT == ole.VT_BOOL && v.Val&0xffff == 0
}
