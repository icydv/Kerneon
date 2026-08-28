# Third-party notices

Kerneon's shipped executable uses only the Go standard library and Windows system DLLs. It has no third-party runtime Go modules and does not download executable components at runtime.

## Go

The Go toolchain and standard library are copyright The Go Authors and distributed under a BSD-style license. The toolchain is used to build Kerneon; its full source and license are available from the Go project.

## go-winres

`github.com/tc-hib/go-winres` version 0.3.3 is an optional build-time utility used to compile the Windows manifest, icon, and version metadata into `rsrc_windows_amd64.syso`. It is licensed under the Zero-Clause BSD license. The utility and its own executable are not embedded in or distributed with Kerneon.

## Visual asset provenance

The final product icon is reproducibly rendered from `cmd/icon/main.go` using the Go standard library. An earlier AI-generated concept was rejected and is not part of the product or release package.
