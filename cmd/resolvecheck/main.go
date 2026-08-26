package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"tellarr/internal/linkresolver"
)

func main() {
	url := "https://hubcloud.cx/drive/vjzhkv1q6zrkvpb"
	if len(os.Args) > 1 {
		url = os.Args[1]
	}
	fmt.Println("resolving:", url)
	// CAPTCHA solving (up to ~2 min) plus provider-side link generation can
	// legitimately take a few minutes end to end.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := linkresolver.Resolve(ctx, url)
	if err != nil {
		fmt.Println("ERROR:", err)
		os.Exit(1)
	}
	fmt.Printf("URL:  %s\nNAME: %s\nSIZE: %d\nHEADERS: %v\n", res.URL, res.Filename, res.Size, res.Headers)
}
