package main

func main() {
	go StartHttp(3300)
	StartSocket(3301)
}
