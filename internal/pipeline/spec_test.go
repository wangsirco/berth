package pipeline

import (
	"strings"
	"testing"
)

const example = `name: acme-web
runner: { memory: 6g, cpus: 4 }
steps:
  - name: typecheck
    image: node:24-bookworm
    run: cd web && npm ci && npx tsc --noEmit
  - name: build
    image: node:24-bookworm
    cache: [web/node_modules, web/.next/cache]
    env: { NEXT_BUILD_CPUS: "2" }
    run: cd web && npx next build
  - name: image
    docker_build: { context: web, target: runtime, platforms: [linux/amd64], push: registry.berth.lan/acme/web }
deploy:
  - host: prod
    compose: /opt/acme/docker-compose.yml
    service: web
    healthcheck: { url: http://127.0.0.1:3000/zh-HK, timeout: 120s }
    rollback: auto
`

func TestParseDesignExample(t *testing.T) {
	s, err := Parse([]byte(example))
	if err != nil {
		t.Fatal(err)
	}
	if s.Runner.MemoryBytes() != 6<<30 || len(s.Steps) != 3 || s.Image() != "registry.berth.lan/acme/web" || s.Deploy[0].Healthcheck.Timeout != "120s" {
		t.Fatalf("%+v", s)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"unknown field":    "name: x\nsteps: [{image: a, run: b}]\nbogus: 1\n",
		"no steps":         "name: x\nsteps: []\n",
		"bad name":         "name: X Y\nsteps: [{image: a, run: b}]\n",
		"escaping cache":   "name: x\nsteps: [{image: a, run: b, cache: [../etc]}]\n",
		"absolute cache":   "name: x\nsteps: [{image: a, run: b, cache: [/root]}]\n",
		"both kinds":       "name: x\nsteps: [{image: a, run: b, docker_build: {context: .}}]\n",
		"bad platform":     "name: x\nsteps: [{docker_build: {context: ., platforms: [windows/amd64]}}]\n",
		"deploy no image":  "name: x\nsteps: [{image: a, run: b}]\ndeploy: [{host: h, compose: /a.yml, service: s}]\n",
		"relative compose": "name: x\nsteps: [{docker_build: {push: r/x}}]\ndeploy: [{host: h, compose: a.yml, service: s}]\n",
		"huge memory":      "name: x\nrunner: {memory: 9999g}\nsteps: [{image: a, run: b}]\n",
		"dup step":         "name: x\nsteps: [{name: a, image: a, run: b}, {name: a, image: a, run: b}]\n",
	}
	for name, y := range cases {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: accepted", name)
		} else if !strings.HasPrefix(err.Error(), "pipeline.") {
			t.Errorf("%s: %v", name, err)
		}
	}
}
