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
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := linkresolver.Resolve(ctx, url)
	if err != nil {
		fmt.Println("ERROR:", err)
		os.Exit(1)
	}
	fmt.Printf("URL:  %s\nNAME: %s\nSIZE: %d\nHEADERS: %v\n", res.URL, res.Filename, res.Size, res.Headers)
}
