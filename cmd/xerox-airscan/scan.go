package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"xerox-airscan/internal/xerox"
)

func scan(ctx context.Context, o options, info xerox.Info, stdout io.Writer) error {
	format, err := imageFormat(o.output)
	if err != nil {
		return err
	}
	height := info.PlatenHeight / 4
	if o.source == "Feeder" {
		height = info.ADFHeight / 4
	}
	settings := xerox.Settings{DPI: o.dpi, Width: info.Width / 4, Height: height, Source: o.source, Gray: o.gray, Format: format}
	if err := settings.Validate(info); err != nil {
		return err
	}
	duration := 5 * time.Minute
	if settings.Source == "Feeder" {
		duration = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	client, err := xerox.Dial(ctx, o.config.ScannerAddress, 20*time.Second)
	if err != nil {
		return err
	}
	defer client.Close()
	for page := 1; ; page++ {
		path := o.output
		if page > 1 {
			ext := filepath.Ext(path)
			path = strings.TrimSuffix(path, ext) + fmt.Sprintf("-%03d", page) + ext
		}
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("output file already exists: %s", path)
		} else if !os.IsNotExist(err) {
			return err
		}
		img, err := client.Scan(settings)
		if errors.Is(err, xerox.ErrNoPaper) && settings.Source == "Feeder" && page > 1 {
			fmt.Fprintf(stdout, "Finished feeder batch: %d pages\n", page-1)
			return nil
		}
		if err != nil {
			return err
		}
		if err := writeImage(path, img); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Saved %dx%d scan to %s\n", img.Bounds().Dx(), img.Bounds().Dy(), path)
		if settings.Source != "Feeder" {
			return nil
		}
	}
}

func imageFormat(path string) (string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png", nil
	case ".jpg", ".jpeg":
		return "image/jpeg", nil
	default:
		return "", errors.New("output filename must end in .png, .jpg, or .jpeg")
	}
}

func writeImage(path string, img image.Image) error {
	format, err := imageFormat(path)
	if err != nil {
		return err
	}
	// Avoid overwriting the user's existing scan. Partially encoded files are removed.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if format == "image/png" {
		err = png.Encode(f, img)
	} else {
		err = jpeg.Encode(f, img, &jpeg.Options{Quality: 95})
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}
