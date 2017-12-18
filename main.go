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
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sync/errgroup"
)

var pathSanitizer = strings.NewReplacer(":", "-")

func newPath(path string) string {
	dir, file := filepath.Split(path)
	file = "." + pathSanitizer.Replace(file) + ".mp3"
	return dir + file
}

var variableSeperator = []byte{'='}

func convert(ctx context.Context, wrk workUnit) error {
	cmd := exec.CommandContext(ctx, "metaflac", "--export-tags-to=-", wrk.path)

	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, os.Stderr

	if err := cmd.Run(); err != nil {
		return err
	}

	s := bufio.NewScanner(&buf)
	meta := make(map[string]string)

	for s.Scan() {
		tok := bytes.SplitN(s.Bytes(), variableSeperator, 2)
		if len(tok) < 2 {
			return errors.New("invalid variable format")
		}

		meta[string(tok[0])] = string(tok[1])
	}

	if s.Err() != nil {
		return s.Err()
	}

	eg, ctx := errgroup.WithContext(ctx)

	cmd1 := exec.CommandContext(ctx, "flac", "-c", "-d", wrk.path)
	cmd2 := exec.CommandContext(ctx, "lame", "-b", "192", "-h",
		"--tt", meta["TITLE"],
		"--tn", meta["TRACKNUMBER"],
		"--tg", meta["GENRE"],
		"--ta", meta["ARTIST"],
		"--tl", meta["ALBUM"],
		"--ty", meta["DATE"],
		"--add-id3v2",
		"-", newPath(wrk.path))

	cmd1.Stderr = os.Stderr
	cmd2.Stdout, cmd2.Stderr = os.Stdout, os.Stderr

	var err error
	if cmd2.Stdin, err = cmd1.StdoutPipe(); err != nil {
		return err
	}

	eg.Go(cmd1.Run)
	eg.Go(cmd2.Run)

	if eg.Wait() == nil {
		return nil
	}

	os.Remove(newPath(wrk.path))
	return eg.Wait()
}

func worker(ctx context.Context, ch chan workUnit, wg *sync.WaitGroup) {
	for work := range ch {
		if err := convert(ctx, work); err != nil {
			fmt.Fprintf(os.Stderr, "<%s>: %v\n", work.path, err)
		}

		wg.Done()
	}
}

type workUnit struct {
	path string
}

func main() {
	recurse := flag.Bool("recurse", true, "whether to walk into child directories")
	flag.Parse()

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

		if filepath.Ext(path) != ".flac" {
			return nil
		}

		if infoOut, err := os.Stat(newPath(path)); err == nil {
			if infoIn, err := os.Stat(path); err != nil {
				return err
			} else if infoIn.ModTime().Before(infoOut.ModTime()) {
				return nil
			}
		} else if !os.IsNotExist(err) {
			return err
		}

		wg.Add(1)
		work <- workUnit{path}
		return nil
	}); err != nil {
		panic(err)
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
