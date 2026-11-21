FROM docker.io/library/golang:1.27.4-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=0 go build -mod=readonly -trimpath -o /lab-groups .

FROM docker.io/hashicorp/nomad-autoscaler:0.6.0@sha256:1140c079acaa8cfc0769f7add3553ea06f589673c4500d2139e01aa447575cad
COPY --from=build /lab-groups /plugins/lab-groups
COPY autoscaler.hcl /etc/autoscaler.hcl
CMD ["agent", "-config=/etc/autoscaler.hcl"]
