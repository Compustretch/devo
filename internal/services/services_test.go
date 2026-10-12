package services

import "testing"

// Every service ships with a smoke test for each supported language. Adding a
// service, or a language to SmokeLanguages, fails this until the tests exist.
func TestEveryServiceHasSmokeTests(t *testing.T) {
	for _, name := range Names() {
		svc, _ := Get(name)
		for _, lang := range SmokeLanguages {
			if len(svc.Smoke[lang]) == 0 {
				t.Errorf("service %q has no %s smoke test; every service must ship one", name, lang)
			}
		}
	}
}
