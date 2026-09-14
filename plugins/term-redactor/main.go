package main

import "github.com/xolo-gateway/xolo/pkg/pluginsdk"

func main() {
	p := newPlugin()
	pluginsdk.ServeWithUI(p, "term-redactor", newUIHandler())
}
