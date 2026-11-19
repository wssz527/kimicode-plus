package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	maxExpandedBytes = 2*maxArchiveBytes + (8 << 20)
	licenseName      = "LICENSE"
)

type extractedFiles struct {
	binary  string
	notices string
	license string
}

type archiveEntry struct {
	name string
	size int64
	mode fs.FileMode
	open func() (io.ReadCloser, error)
}

func extractArchive(archivePath, stage, targetOS, targetArch string) (extractedFiles, error) {
	var files extractedFiles
	seen := make(map[string]bool)
	var expanded int64
	name := "enhanced-kimi"
	targetName := name + "-" + targetOS + "-" + targetArch
	if targetOS == "windows" {
		name += ".exe"
		targetName += ".exe"
	}
	visit := func(entry archiveEntry) error {
		clean := strings.TrimSuffix(entry.name, "/")
		if strings.ContainsAny(clean, "\\:\x00") || !fs.ValidPath(clean) || clean == "." {
			return fmt.Errorf("安装包含有不安全的路径 %q", entry.name)
		}
		if !entry.mode.IsRegular() && !entry.mode.IsDir() {
			return fmt.Errorf("安装包含有链接或不支持的文件 %q", entry.name)
		}
		if entry.size < 0 || entry.size > maxArchiveBytes || expanded+entry.size > maxExpandedBytes {
			return errors.New("安装包解压大小超过限制")
		}
		expanded += entry.size
		if entry.mode.IsDir() {
			return nil
		}
		key := clean
		if targetOS == "windows" {
			key = strings.ToLower(clean)
		}
		if seen[key] || len(seen) >= 4096 {
			return errors.New("安装包含有重复文件或文件数量超过限制")
		}
		seen[key] = true
		var file *string
		var destinationName string
		switch path.Base(clean) {
		case name, targetName:
			file, destinationName = &files.binary, "binary"
		case noticesName:
			file, destinationName = &files.notices, noticesName
		case licenseName:
			file, destinationName = &files.license, licenseName
		default:
			return nil
		}
		if *file != "" {
			return fmt.Errorf("安装包含有多份 %s", destinationName)
		}
		destination := filepath.Join(stage, destinationName)
		body, err := entry.open()
		if err != nil {
			return err
		}
		defer body.Close()
		out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		count, copyErr := io.Copy(out, io.LimitReader(body, entry.size+1))
		if err = errors.Join(copyErr, out.Close()); err != nil {
			return err
		}
		if count != entry.size {
			return errors.New("安装包中声明的文件大小与实际内容不符")
		}
		*file = destination
		return nil
	}
	var err error
	if targetOS == "windows" {
		err = visitZip(archivePath, visit)
	} else {
		err = visitTar(archivePath, visit)
	}
	if err != nil {
		return extractedFiles{}, err
	}
	if files.binary == "" {
		return extractedFiles{}, errors.New("安装包中缺少增强核心程序")
	}
	if err = verifyMagic(files.binary, targetOS); err != nil {
		return extractedFiles{}, err
	}
	return files, nil
}

func visitZip(filename string, visit func(archiveEntry) error) error {
	archive, err := zip.OpenReader(filename)
	if err != nil {
		return err
	}
	defer archive.Close()
	if len(archive.File) > 4096 {
		return errors.New("安装包文件数量超过限制")
	}
	for _, entry := range archive.File {
		if entry.UncompressedSize64 > maxArchiveBytes {
			return errors.New("安装包解压大小超过限制")
		}
		if err = visit(archiveEntry{entry.Name, int64(entry.UncompressedSize64), entry.Mode(), entry.Open}); err != nil {
			return err
		}
	}
	return nil
}

func visitTar(filename string, visit func(archiveEntry) error) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer compressed.Close()
	limited := &io.LimitedReader{R: compressed, N: maxExpandedBytes + 1}
	archive := tar.NewReader(limited)
	for count := 0; ; count++ {
		header, nextErr := archive.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return nextErr
		}
		if count >= 4096 {
			return errors.New("安装包文件数量超过限制")
		}
		switch header.Typeflag {
		case tar.TypeReg, tar.TypeDir:
		default:
			return fmt.Errorf("安装包含有链接或不支持的文件 %q", header.Name)
		}
		entry := archiveEntry{header.Name, header.Size, header.FileInfo().Mode(), func() (io.ReadCloser, error) { return io.NopCloser(archive), nil }}
		if err = visit(entry); err != nil {
			return err
		}
	}
	if _, err = io.Copy(io.Discard, limited); err != nil {
		return err
	}
	if limited.N == 0 {
		return errors.New("安装包解压大小超过限制")
	}
	return nil
}

func verifyMagic(filename, targetOS string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	var magic [4]byte
	size, err := io.ReadFull(file, magic[:])
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return err
	}
	valid := false
	switch targetOS {
	case "windows":
		valid = size >= 2 && string(magic[:2]) == "MZ"
	case "linux":
		valid = size == 4 && string(magic[:]) == "\x7fELF"
	case "darwin":
		switch binary.BigEndian.Uint32(magic[:]) {
		case 0xfeedface, 0xcefaedfe, 0xfeedfacf, 0xcffaedfe, 0xcafebabe, 0xbebafeca, 0xcafebabf, 0xbfbafeca:
			valid = size == 4
		}
	}
	if !valid {
		return errors.New("安装包中的核心程序格式与当前系统不符")
	}
	return nil
}
