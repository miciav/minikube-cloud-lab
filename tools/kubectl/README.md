# Containerized kubectl

`main.go` is the unmodified upstream entrypoint from Kubernetes v1.35.10:
https://github.com/kubernetes/kubernetes/blob/v1.35.10/cmd/kubectl/kubectl.go

This separate module keeps kubectl on the cluster's 1.35 minor version while
rebuilding it with Go 1.27.3 and patched `golang.org/x/net` and `golang.org/x/text`.
The upstream v1.35.10 image still contains versions flagged by the CI scanner.
Keep the Kubernetes module versions, source entrypoint and Dockerfile version
metadata aligned when upgrading. Commit `go.mod` and `go.sum` after running
`go -C tools/kubectl mod tidy`.

CI runs the client in the final lab image and scans that image with Trivy.
