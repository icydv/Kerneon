//go:build windows

package main

import (
	"errors"
	"strings"
	"syscall"
	"unsafe"
)

const (
	credentialTarget        = "Kerneon/OpenAI API Key"
	credTypeGeneric         = 1
	credPersistLocalMachine = 2
)

type windowsCredential struct {
	Flags, Type        uint32
	TargetName         *uint16
	Comment            *uint16
	LastWrittenLow     uint32
	LastWrittenHigh    uint32
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

var (
	procCredWriteW  = advapi32.NewProc("CredWriteW")
	procCredReadW   = advapi32.NewProc("CredReadW")
	procCredDeleteW = advapi32.NewProc("CredDeleteW")
	procCredFree    = advapi32.NewProc("CredFree")
)

func saveKerneonCredential(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 2048 {
		return errors.New("API key is empty or too long")
	}
	blob := []byte(value)
	c := windowsCredential{Type: credTypeGeneric, TargetName: utf16Ptr(credentialTarget), Comment: utf16Ptr("Optional Kerneon AI insights"), CredentialBlobSize: uint32(len(blob)), CredentialBlob: &blob[0], Persist: credPersistLocalMachine, UserName: utf16Ptr("Kerneon")}
	if ok, _, err := procCredWriteW.Call(uintptr(unsafe.Pointer(&c)), 0); ok == 0 {
		return err
	}
	return nil
}

func readKerneonCredential() (string, error) {
	var ptr uintptr
	if ok, _, err := procCredReadW.Call(uintptr(unsafe.Pointer(utf16Ptr(credentialTarget))), credTypeGeneric, 0, uintptr(unsafe.Pointer(&ptr))); ok == 0 {
		return "", err
	}
	defer procCredFree.Call(ptr)
	var c windowsCredential
	procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&c)), ptr, unsafe.Sizeof(c))
	if c.CredentialBlob == nil || c.CredentialBlobSize == 0 {
		return "", errors.New("stored credential is empty")
	}
	b := unsafe.Slice(c.CredentialBlob, int(c.CredentialBlobSize))
	return string(append([]byte(nil), b...)), nil
}

func deleteKerneonCredential() error {
	if ok, _, err := procCredDeleteW.Call(uintptr(unsafe.Pointer(utf16Ptr(credentialTarget))), credTypeGeneric, 0); ok == 0 {
		if errno, yes := err.(syscall.Errno); yes && errno == 1168 {
			return nil
		}
		return err
	}
	return nil
}
