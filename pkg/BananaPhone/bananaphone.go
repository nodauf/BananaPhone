package bananaphone

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Binject/debug/pe"
	"github.com/awgh/rawreader"
)

//PhoneMode determines the way a bananaphone will resolve sysids
type PhoneMode int

const (
	//MemoryBananaPhoneMode will resolve by finding the PEB in-memory, and enumerating the loaded ntdll.dll to resolve exports and determine the sysid.
	MemoryBananaPhoneMode PhoneMode = iota
	//DiskBananaPhoneMode will resolve by loading ntdll.dll from disk, and enumerating to resolve exports and determine the sysid.
	DiskBananaPhoneMode
	//AutoBananaPhoneMode will resolve by first trying to resolve in-memory, and then falling back to loading from disk if in-memory fails (eg, if it's hooked and the sysid's have been moved).
	AutoBananaPhoneMode
	//HalosGateBananaPhoneMode will resolve by first trying to resolve in-memory, and then falling back to deduce the syscall by searching a non-hooked function
	HalosGateBananaPhoneMode
)

//BananaPhone will resolve SysID's used for syscalls while making minimal API calls. These ID's can be used for functions like NtAllocateVirtualMemory as defined in functions.go.
type BananaPhone struct {
	banana      *pe.File
	isAuto      bool
	isHalosGate bool
	memloc      uintptr
}

//NewBananaPhone creates a new instance of a bananaphone with behaviour as defined by the input value. Use AutoBananaPhoneMode if you're not sure.
/*
Possible values:
	- MemoryBananaPhoneMode
	- DiskBananaPhoneMode
	- AutoBananaPhoneMode
	- HalosGate
*/
func NewBananaPhone(t PhoneMode) (*BananaPhone, error) {
	return NewBananaPhoneNamed(t, "ntdll.dll", `C:\Windows\system32\ntdll.dll`)
}

//NewSystemBananaPhoneNamed is literally just an un-error handled passthrough for NewBananaPhoneNamed to easily work with mkwinsyscall. The ptr might be nil, who knows! lol! yolo!
func NewSystemBananaPhoneNamed(t PhoneMode, name, diskpath string) *BananaPhone {
	r, _ := NewBananaPhoneNamed(t, name, diskpath)
	return r
}

//NewBananaPhoneNamed creates a new instance of a bananaphone with behaviour as defined by the input value, specifying the module provided. Use AutoBananaPhoneMode if you're not sure which mode and specify the path. Path only used for disk/auto modes.
/*
Possible values:
	- MemoryBananaPhoneMode
	- DiskBananaPhoneMode
	- AutoBananaPhoneMode
	- HalosGate
*/
func NewBananaPhoneNamed(t PhoneMode, name, diskpath string) (*BananaPhone, error) {
	var p *pe.File
	var e error
	var bp = &BananaPhone{}
	switch t {
	case HalosGateBananaPhoneMode:
		fallthrough
	case AutoBananaPhoneMode:
		fallthrough
	case MemoryBananaPhoneMode:
		loads, err := InMemLoads()
		if err != nil {
			return nil, err
		}
		found := false
		for k, load := range loads { //shout out to Frank Reynolds
			if strings.ToLower(k) == strings.ToLower(diskpath) || strings.ToLower(name) == strings.ToLower(filepath.Base(k)) {
				rr := rawreader.New(uintptr(load.BaseAddr), int(load.Size))
				p, e = pe.NewFileFromMemory(rr)
				bp.memloc = uintptr(load.BaseAddr)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("module not found, bad times (%s %s)", diskpath, filepath.Base(diskpath))
		}
	case DiskBananaPhoneMode:
		p, e = pe.Open(diskpath)
	}
	bp.banana = p
	bp.isAuto = t == AutoBananaPhoneMode
	bp.isHalosGate = t == HalosGateBananaPhoneMode
	return bp, e
}

//GetFuncPtr returns a pointer to the function (Virtual Address)
func (b *BananaPhone) GetFuncPtr(funcname string) (uint64, error) {
	exports, err := b.banana.Exports()
	if err != nil {
		return 0, err
	}
	for _, ex := range exports {
		if strings.ToLower(funcname) == strings.ToLower(ex.Name) {
			return uint64(b.memloc) + uint64(ex.VirtualAddress), nil
		}
	}
	return 0, fmt.Errorf("could not find function: %s", funcname)
}

//BananaProc emulates the windows proc thing
type BananaProcedure struct {
	address uintptr
}

//Addr returns the address of this procedure
func (b BananaProcedure) Addr() uintptr {
	return b.address
}

//NewProc emulates the windows NewProc call :-)
func (b *BananaPhone) NewProc(funcname string) BananaProcedure {
	addr, _ := b.GetFuncPtr(funcname) //yolo error handling
	return BananaProcedure{address: uintptr(addr)}
}

//GetSysID resolves the provided function name into a sysid.
func (b *BananaPhone) GetSysID(funcname string) (uint16, error) {
	r, e := b.getSysID(funcname, 0, false)
	if e != nil {
		var err MayBeHookedError
		if b.isAuto && errors.As(e, &err) {
			var e2 error
			b.banana, e2 = pe.Open(`C:\Windows\system32\ntdll.dll`)
			if e2 != nil {
				return 0, e2
			}
			r, e = b.getSysID(funcname, 0, false)
		} else if b.isHalosGate && errors.As(e, &err) {
			r, e = b.getSysIDFromNeighbor(funcname, 0, false)
		}
	}
	return r, e
}

//GetSysIDOrd resolves the provided ordinal into a sysid.
func (b *BananaPhone) GetSysIDOrd(ordinal uint32) (uint16, error) {
	r, e := b.getSysID("", ordinal, true)
	if e != nil {
		var err MayBeHookedError
		if b.isAuto && errors.Is(e, &err) {
			var e2 error
			b.banana, e2 = pe.Open(`C:\Windows\system32\ntdll.dll`)
			if e2 != nil {
				return 0, e2
			}
			r, e = b.getSysID("", ordinal, true)
		}
	}
	return r, e
}

//getSysID does the heavy lifting - will resolve a name or ordinal into a sysid by getting exports, and parsing the first few bytes of the function to extract the ID. Doens't look at the ord value unless useOrd is set to true.
func (b BananaPhone) getSysID(funcname string, ord uint32, useOrd bool) (uint16, error) {
	ex, e := b.banana.Exports()
	if e != nil {
		return 0, e
	}

	for _, exp := range ex {
		if (useOrd && exp.Ordinal == ord) || // many bothans died for this feature (thanks awgh). Turns out that a value can be exported by ordinal, but not by name! man I love PE files. ha ha jk.
			exp.Name == funcname {
			offset := rvaToOffset(b.banana, exp.VirtualAddress)
			b, e := b.banana.Bytes()
			if e != nil {
				return 0, e
			}
			buff := b[offset : offset+10]

			return sysIDFromRawBytes(buff)
		}
	}
	return 0, errors.New("Could not find syscall ID")
}

//getSysIDFromNeighboor deduces the syscall ID based on the a neighbor's syscall that is not hooked
func (b BananaPhone) getSysIDFromNeighbor(funcname string, ord uint32, useOrd bool) (uint16, error) {

	ex, e := b.banana.Exports()
	if e != nil {
		return 0, e
	}

	for _, exp := range ex {
		if (useOrd && exp.Ordinal == ord) || // many bothans died for this feature (thanks awgh). Turns out that a value can be exported by ordinal, but not by name! man I love PE files. ha ha jk.
			exp.Name == funcname {
			offset := rvaToOffset(b.banana, exp.VirtualAddress)
			bBytes, e := b.banana.Bytes()
			if e != nil {
				return 0, e
			}
			buff := bBytes[offset : offset+10]

			sysId, e := sysIDFromRawBytes(buff)
			var err MayBeHookedError
			// Look for the syscall ID in the neighborhood
			if errors.As(e, &err) {
				start, size := GetNtdllStart()
				distanceNeighbor := 0
				// Search forward
				for i := uintptr(offset); i < start+size; i += 1 {
					if bBytes[i] == byte('\x0f') && bBytes[i+1] == byte('\x05') && bBytes[i+2] == byte('\xc3') {
						distanceNeighbor++
						// The sysid should be located 14 bytes after the syscall; ret instruction.
						sysId, e := sysIDFromRawBytes(bBytes[i+14 : i+14+8])
						if !errors.As(e, &err) {
							return sysId - uint16(distanceNeighbor), e
						}
					}
				}
				// reset the value to 1. When we go forward we catch the current syscall; ret but not when we go backward, so distanceNeighboor = 0 for forward and distanceNeighboor = 1 for backward
				distanceNeighbor = 1
				// If nothing has been found forward, search backward
				for i := uintptr(offset) - 1; i > 0; i -= 1 {
					if bBytes[i] == byte('\x0f') && bBytes[i+1] == byte('\x05') && bBytes[i+2] == byte('\xc3') {
						distanceNeighbor++
						// The sysid should be located 14 bytes after the syscall; ret instruction.
						sysId, e := sysIDFromRawBytes(bBytes[i+14 : i+14+8])
						if !errors.As(e, &err) {
							return sysId + uint16(distanceNeighbor) - 1, e
						}
					}
				}
			} else {
				return sysId, e
			}
		}
	}
	return 0, errors.New("Could not find syscall ID")
}

//MayBeHookedError an error returned when trying to extract the sysid from a resolved function. Contains the bytes that were actually found (incase it's useful to someone?)
type MayBeHookedError struct {
	Foundbytes []byte
}

func (e MayBeHookedError) Error() string {
	return fmt.Sprintf("may be hooked: wanted %x got %x", HookCheck, e.Foundbytes)
}

//HookCheck is the bytes expected to be seen at the start of the function:
/*
	mov r10, rcx ;(4c 8b d1)
	mov eax, sysid ;(b8 sysid)
*/
var HookCheck = []byte{0x4c, 0x8b, 0xd1, 0xb8}
