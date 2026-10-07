//go:build e2e

// Command fixture seeds or removes e2e data on Telegram's test servers:
//
//	go run -tags e2e ./e2e/cmd/fixture seed   # create forum, topic, messages, media, pin, folder
//	go run -tags e2e ./e2e/cmd/fixture show   # print what the fixture holds
//	go run -tags e2e ./e2e/cmd/fixture clean  # delete them and the fixture file
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/gotd/td/tg"

	"github.com/joeunderwood/tgbup/e2e/testenv"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: fixture seed|show|clean")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "seed":
		err = seed()
	case "show":
		err = show()
	case "clean":
		err = clean()
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "fixture:", err)
		os.Exit(1)
	}
}

func seed() error {
	if _, err := os.Stat(testenv.FixturePath()); err == nil {
		return fmt.Errorf("%s already exists: run `make e2e-clean` first", testenv.FixturePath())
	}
	var f testenv.Fixture
	err := testenv.WithTelegram(func(ctx context.Context, raw *tg.Client) error {
		var err error
		f, err = testenv.Seed(ctx, raw)
		return err
	})
	// Save even a partial fixture so `clean` can remove whatever was created.
	if f.ChannelID != 0 {
		if serr := testenv.SaveFixture(f); serr != nil {
			return errors.Join(err, serr)
		}
	}
	if err != nil {
		return err
	}
	fmt.Printf("seeded forum %q in folder %q on the test servers\n", f.Title, f.Folder)
	return show()
}

func show() error {
	b, err := os.ReadFile(testenv.FixturePath())
	if err != nil {
		return err
	}
	fmt.Printf("%s:\n%s", testenv.FixturePath(), b)
	return nil
}

func clean() error {
	f, err := testenv.LoadFixture()
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Println("nothing to clean")
		return nil
	}
	if err != nil {
		return err
	}
	if err := testenv.WithTelegram(func(ctx context.Context, raw *tg.Client) error {
		return testenv.Cleanup(ctx, raw, f)
	}); err != nil {
		return err
	}
	if err := os.Remove(testenv.FixturePath()); err != nil {
		return err
	}
	fmt.Printf("deleted forum %q and folder %q\n", f.Title, f.Folder)
	return nil
}
