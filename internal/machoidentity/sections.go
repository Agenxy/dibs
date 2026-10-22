// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright 2026 Agenxy

// Package machoidentity compares executable code independently of its code
// signature. Signing the same image as nested code changes its load commands
// and signature, but must not change its sections.
package machoidentity

import (
	"crypto/sha256"
	"debug/macho"
	"fmt"
	"io"
	"os"
	"sort"
)

// Sections fingerprints section names, layout and bytes for every architecture.
// Link-edit data and load commands are deliberately outside the comparison.
func Sections(r io.ReaderAt) (string, error) {
	var images []*macho.File
	if fat, err := macho.NewFatFile(r); err == nil {
		for _, a := range fat.Arches {
			images = append(images, a.File)
		}
	} else {
		image, err := macho.NewFile(r)
		if err != nil {
			return "", err
		}
		images = append(images, image)
	}
	var records []string
	for _, image := range images {
		if len(image.Sections) == 0 {
			return "", fmt.Errorf("Mach-O %s has no sections", image.Cpu)
		}
		for _, section := range image.Sections {
			h := sha256.New()
			// Zero-fill sections have no file bytes. Their declared size is
			// included below; reading offset zero would hash the header instead.
			kind := section.Flags & 0xff
			if kind != 1 && kind != 0xc && kind != 0x12 {
				if _, err := io.Copy(h, section.Open()); err != nil {
					return "", err
				}
			}
			records = append(records, fmt.Sprintf("%d/%d %s/%s %x %x %x %x %x", image.Cpu, image.SubCpu,
				section.Seg, section.Name, section.Addr, section.Size, section.Align, section.Flags, h.Sum(nil)))
		}
	}
	sort.Strings(records)
	h := sha256.New()
	for _, record := range records {
		_, _ = fmt.Fprintln(h, record)
	}
	return fmt.Sprintf("sha256:%x (%d sections)", h.Sum(nil), len(records)), nil
}

// File compares without reading two whole daemon images into memory.
func File(path string) (string, error) {
	f, err := os.Open(path) // #nosec G304,G703 -- own executable or build artifact, never participant input
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	return Sections(f)
}
