//go:build windows

package printing

import (
	"context"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows raw printing via the print spooler's "RAW" datatype — the direct
// equivalent of CUPS' `lp -o raw`. The bytes bypass driver rendering, so ESC/POS
// and TSPL streams reach the printer unmodified. Done with winspool.drv syscalls
// so the binary stays pure Go (no CGO).
var (
	winspool             = windows.NewLazySystemDLL("winspool.drv")
	procOpenPrinter      = winspool.NewProc("OpenPrinterW")
	procClosePrinter     = winspool.NewProc("ClosePrinter")
	procStartDocPrinter  = winspool.NewProc("StartDocPrinterW")
	procEndDocPrinter    = winspool.NewProc("EndDocPrinter")
	procStartPagePrinter = winspool.NewProc("StartPagePrinter")
	procEndPagePrinter   = winspool.NewProc("EndPagePrinter")
	procWritePrinter     = winspool.NewProc("WritePrinter")
	procEnumPrinters     = winspool.NewProc("EnumPrintersW")
	procGetDefaultPrint  = winspool.NewProc("GetDefaultPrinterW")
)

// docInfo1 mirrors Win32 DOC_INFO_1 (level 1).
type docInfo1 struct {
	pDocName    *uint16
	pOutputFile *uint16
	pDatatype   *uint16
}

// printerInfo4 mirrors Win32 PRINTER_INFO_4 (level 4) — enough for names.
type printerInfo4 struct {
	pPrinterName *uint16
	pServerName  *uint16
	attributes   uint32
}

const (
	printerEnumLocal       = 0x00000002
	printerEnumConnections = 0x00000004
)

// rawSpool sends data to a Windows printer using the RAW datatype. An empty
// queue uses the OS default printer.
func rawSpool(ctx context.Context, queue string, data []byte) error {
	if queue == "" {
		def, err := defaultPrinter()
		if err != nil {
			return err
		}
		queue = def
	}
	name, err := windows.UTF16PtrFromString(queue)
	if err != nil {
		return fmt.Errorf("bad printer name %q: %w", queue, err)
	}

	var h windows.Handle
	if r, _, e := procOpenPrinter.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&h)), 0); r == 0 {
		return fmt.Errorf("open printer %q: %w", queue, e)
	}
	defer procClosePrinter.Call(uintptr(h))

	docName, _ := windows.UTF16PtrFromString("Karots POS")
	datatype, _ := windows.UTF16PtrFromString("RAW")
	di := docInfo1{pDocName: docName, pDatatype: datatype}
	if r, _, e := procStartDocPrinter.Call(uintptr(h), 1, uintptr(unsafe.Pointer(&di))); r == 0 {
		return fmt.Errorf("start document on %q: %w", queue, e)
	}
	defer procEndDocPrinter.Call(uintptr(h))

	if r, _, e := procStartPagePrinter.Call(uintptr(h)); r == 0 {
		return fmt.Errorf("start page on %q: %w", queue, e)
	}
	defer procEndPagePrinter.Call(uintptr(h))

	if len(data) == 0 {
		return nil
	}
	var written uint32
	if r, _, e := procWritePrinter.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(len(data)),
		uintptr(unsafe.Pointer(&written)),
	); r == 0 {
		return fmt.Errorf("write to printer %q: %w", queue, e)
	}
	return nil
}

// osQueues lists printers installed in Windows via EnumPrinters (level 4).
func osQueues(ctx context.Context) []string {
	const flags = printerEnumLocal | printerEnumConnections
	var needed, returned uint32
	// First call sizes the buffer (expected to "fail" with a non-zero needed).
	procEnumPrinters.Call(flags, 0, 4, 0, 0, uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&returned)))
	if needed == 0 {
		return nil
	}
	buf := make([]byte, needed)
	if r, _, _ := procEnumPrinters.Call(
		flags, 0, 4,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(needed),
		uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&returned)),
	); r == 0 {
		return nil
	}
	infos := unsafe.Slice((*printerInfo4)(unsafe.Pointer(&buf[0])), int(returned))
	names := make([]string, 0, returned)
	for _, pi := range infos {
		if pi.pPrinterName != nil {
			names = append(names, windows.UTF16PtrToString(pi.pPrinterName))
		}
	}
	return names
}

// defaultPrinter returns the OS default printer name.
func defaultPrinter() (string, error) {
	var size uint32
	procGetDefaultPrint.Call(0, uintptr(unsafe.Pointer(&size)))
	if size == 0 {
		return "", fmt.Errorf("no default printer is set")
	}
	buf := make([]uint16, size)
	if r, _, e := procGetDefaultPrint.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); r == 0 {
		return "", fmt.Errorf("get default printer: %w", e)
	}
	return windows.UTF16ToString(buf), nil
}
