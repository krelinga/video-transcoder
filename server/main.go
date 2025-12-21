package main

import "github.com/krelinga/video-transcoder/internal"

func main() {
	_ = internal.NewConfigFromEnv()
}