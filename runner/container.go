package runner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	TwispImage      = "public.ecr.aws/twisp/local:latest"
	AdminPort       = "3000"
	HTTPPort        = "8080"
	GRPCPort        = "8081"
	GraphQLEndpoint = "/financial/v1/graphql"
)

// TwispContainer wraps a testcontainer running the Twisp local image.
type TwispContainer struct {
	Container  testcontainers.Container
	GraphQLURL string
	AdminURL   string
	GRPCPort   int
}

// StartTwispContainer starts a new Twisp container and waits for it to be ready.
func StartTwispContainer(ctx context.Context, image string, alwaysPull bool) (*TwispContainer, error) {
	if strings.TrimSpace(image) == "" {
		image = TwispImage
	}

	req := testcontainers.ContainerRequest{
		Image:           image,
		AlwaysPullImage: alwaysPull,
		ExposedPorts:    []string{AdminPort + "/tcp", HTTPPort + "/tcp", GRPCPort + "/tcp"},
		WaitingFor: wait.ForAll(
			wait.ForHTTP("/healthcheck").WithPort(HTTPPort).WithStartupTimeout(2 * time.Minute),
		),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to start container: %w", err)
	}

	httpPort, err := container.MappedPort(ctx, HTTPPort)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("failed to get HTTP port: %w", err)
	}

	adminPort, err := container.MappedPort(ctx, AdminPort)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("failed to get admin port: %w", err)
	}

	grpcPort, err := container.MappedPort(ctx, GRPCPort)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("failed to get gRPC port: %w", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("failed to get container host: %w", err)
	}

	return &TwispContainer{
		Container:  container,
		GraphQLURL: fmt.Sprintf("http://%s:%s%s", host, httpPort.Port(), GraphQLEndpoint),
		AdminURL:   fmt.Sprintf("http://%s:%s", host, adminPort.Port()),
		GRPCPort:   grpcPort.Int(),
	}, nil
}

// Terminate stops and removes the container.
func (c *TwispContainer) Terminate(ctx context.Context) error {
	if c.Container != nil {
		return c.Container.Terminate(ctx)
	}
	return nil
}
