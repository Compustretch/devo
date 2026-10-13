package services

import "testing"

func TestRegistryRejectsPathsOutsideProject(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "files",
			body: `services:
  escape: false
escape:
  description: path traversal
  compose:
    image: busybox:latest
  files:
    "../../outside.txt": "owned by the service"
`,
		},
		{
			name: "smoke test",
			body: `services:
  escape: false
escape:
  description: path traversal
  compose:
    image: busybox:latest
  smoke:
    rust:
      "../../outside.rs": "#[test] fn outside() {}"
`,
		},
		{
			name: "datasources file",
			body: `services:
  escape: false
escape:
  description: path traversal
  compose:
    image: busybox:latest
  datasources_file: ../outside.yaml
`,
		},
		{
			name: "absolute file path",
			body: `services:
  escape: false
escape:
  description: absolute path
  compose:
    image: busybox:latest
  files:
    "/outside.txt": "must be rejected"
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := writeRegistry(t, tc.body)
			if err := Load(p); err == nil {
				t.Fatal("expected a registry path outside the project to be rejected")
			}
		})
	}
}
