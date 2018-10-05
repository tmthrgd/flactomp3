// Copyright 2017 Tom Thorogood. All rights reserved.
// Use of this source code is governed by a
// Modified BSD License license that can be found in
// the LICENSE file.

package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/tmthrgd/fasttemplate"
)

var pathSanitizer = strings.NewReplacer(":", "-")

func newPath(tmpl *fasttemplate.Template, path string) string {
	dir, file := filepath.Split(path)
	ext := filepath.Ext(file)
	name := strings.TrimSuffix(file, ext)

	return tmpl.ExecuteString(map[string]interface{}{
		"path": path,

		"dir": dir,

		"file":  file,
		"@file": pathSanitizer.Replace(file),

		"name":  name,
		"@name": pathSanitizer.Replace(name),

		"ext": ext,
	})
}

var variableSeparator = []byte{'='}

func convert(ctx context.Context, wrk workUnit) error {
	cmd := exec.CommandContext(ctx, "metaflac", "--export-tags-to=-", "--no-utf8-convert", wrk.path)

	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, os.Stderr

	if err := cmd.Run(); err != nil {
		return err
	}

	s := bufio.NewScanner(&buf)
	meta := make(map[string]string)

	for s.Scan() {
		tok := bytes.SplitN(s.Bytes(), variableSeparator, 2)
		if len(tok) < 2 {
			return errors.New("invalid variable format")
		}

		meta[string(bytes.ToUpper(tok[0]))] = string(tok[1])
	}

	if s.Err() != nil {
		return s.Err()
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd1 := exec.CommandContext(ctx, "flac", "-c", "-d", wrk.path)
	cmd2 := exec.CommandContext(ctx, "lame", "-b", "192", "-h",
		"--tt", meta["TITLE"],
		"--tn", meta["TRACKNUMBER"],
		"--tg", meta["GENRE"],
		"--ta", meta["ARTIST"],
		"--tl", meta["ALBUM"],
		"--ty", meta["DATE"],
		"--add-id3v2",
		"-", wrk.newPath)

	cmd1.Stderr = os.Stderr
	cmd2.Stdout, cmd2.Stderr = os.Stdout, os.Stderr

	var err error
	if cmd2.Stdin, err = cmd1.StdoutPipe(); err != nil {
		return err
	}

	if err := cmd2.Start(); err != nil {
		return err
	}

	if err := cmd1.Run(); err != nil {
		os.Remove(wrk.newPath)
		return err
	}

	if err := cmd2.Wait(); err != nil {
		os.Remove(wrk.newPath)
		return err
	}

	return nil
}

func worker(ctx context.Context, ch <-chan workUnit, wg *sync.WaitGroup) {
	for work := range ch {
		if err := convert(ctx, work); err != nil {
			fmt.Fprintf(os.Stderr, "<%s>: %v\n", work.path, err)
		}

		wg.Done()
	}
}

type workUnit struct {
	path, newPath string
}

func fileIsFlac(path string) (bool, error) {
	if filepath.Ext(path) == ".flac" {
		return true, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	var buf [4]byte
	_, err = f.Read(buf[:])
	return string(buf[:]) == "fLaC", err
}

func main() {
	outPath := flag.String("out", "{dir}.{@file}.mp3", "the output path template")
	recurse := flag.Bool("recurse", true, "whether to walk into child directories")
	flag.Parse()

	for _, name := range [...]string{"metaflac", "flac", "lame"} {
		if _, err := exec.LookPath(name); err != nil {
			log.Fatal(err)
		}
	}

	var wg sync.WaitGroup

	work := make(chan workUnit, 32)
	defer close(work)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for i := 0; i < cap(work); i++ {
		go worker(ctx, work, &wg)
	}

	dir := flag.Arg(0)
	if dir == "" {
		dir = "."
	}

	pathTmpl, err := fasttemplate.NewTemplate(*outPath, "{", "}")
	if err != nil {
		log.Fatal(err)
	}

	if err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			if !*recurse && path != dir {
				return filepath.SkipDir
			}

			return nil
		}

		if flac, err := fileIsFlac(path); err != nil || !flac {
			return err
		}

		newPath := newPath(pathTmpl, path)

		if infoOut, err := os.Stat(newPath); err == nil {
			if info.ModTime().Before(infoOut.ModTime()) {
				return nil
			}
		} else if !os.IsNotExist(err) {
			return err
		}

		wg.Add(1)
		work <- workUnit{path, newPath}
		return nil
	}); err != nil {
		log.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		wg.Wait()
	}()

	// termination handler
	term := make(chan os.Signal, 1)
	signal.Notify(term, os.Interrupt, syscall.SIGTERM)

	select {
	case <-done:
	case <-term:
		signal.Stop(term)

		cancel()
		<-done
	}
}
