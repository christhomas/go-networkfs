# The mock API server, built from test/mockapi.
#
# Dropbox, Google Drive and OneDrive cannot be run locally the way Samba or
# MinIO can. This stands in for all three so the drivers that use them can be
# tested without any connected service — including from the C ABI harnesses,
# which are separate programs and cannot reach an in-process fake.
FROM golang:1.25-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/mockapi ./test/mockapi

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/mockapi /mockapi
ENV MOCKAPI_ADDR=:8081
EXPOSE 8081
ENTRYPOINT ["/mockapi"]
