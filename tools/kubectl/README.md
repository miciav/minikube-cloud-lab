# Containerized kubectl

`main.go` is the unmodified upstream entrypoint from Kubernetes v1.37.3:
https://github.com/kubernetes/kubernetes/blob/v1.37.3/cmd/kubectl/kubectl.go

This separate module keeps kubectl on the cluster's 1.37 minor version while
rebuilding it with Go 1.27.4 and patched `golang.org/x/net` and `golang.org/x/text`.
Upstream kubectl images have shipped versions flagged by the CI scanner (v1.35.10 did).
Keep the Kubernetes module versions, source entrypoint and Dockerfile version
metadata aligned when upgrading. Commit `go.mod` and `go.sum` after running
`go -C tools/kubectl mod tidy`.

CI runs the client in the final lab image and scans that image with Trivy.
