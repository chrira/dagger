package sdk

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	"dagger.io/dagger"
	"github.com/dagger/dagger/internal/mage/util"
	"github.com/magefile/mage/mg"
	"golang.org/x/sync/errgroup"
)

var nodejsGeneratedAPIPath = "sdk/nodejs/api/client.gen.ts"

var _ SDK = Nodejs{}

type Nodejs mg.Namespace

// Lint lints the Node.js SDK
func (t Nodejs) Lint(ctx context.Context) error {
	c, err := dagger.Connect(ctx, dagger.WithLogOutput(os.Stderr))
	if err != nil {
		return err
	}
	defer c.Close()

	c = c.Pipeline("sdk").Pipeline("nodejs").Pipeline("lint")

	eg, gctx := errgroup.WithContext(ctx)

	base := nodeJsBase(c)

	eg.Go(func() error {
		_, err = base.WithExec([]string{"yarn", "lint"}).Sync(gctx)
		return err
	})

	eg.Go(func() error {
		path := "docs/current_docs"
		_, err = base.
			WithDirectory(
				fmt.Sprintf("/%s", path),
				util.Repository(c).Directory(path),
				dagger.ContainerWithDirectoryOpts{
					Include: []string{
						"**/*.mts",
						"**/*.mjs",
						"**/*.ts",
						"**/*.js",
						"*prettier*",
						"*eslint*",
					},
				},
			).
			WithExec([]string{"yarn", "docs:lint"}).
			Sync(gctx)
		return err
	})

	eg.Go(func() error {
		return lintGeneratedCode(func() error {
			return t.Generate(gctx)
		}, nodejsGeneratedAPIPath)
	})

	return eg.Wait()
}

// Test tests the Node.js SDK
func (t Nodejs) Test(ctx context.Context) error {
	c, err := dagger.Connect(ctx, dagger.WithLogOutput(os.Stderr))
	if err != nil {
		return err
	}
	defer c.Close()

	c = c.Pipeline("sdk").Pipeline("nodejs").Pipeline("test")

	devEngine, endpoint, err := util.CIDevEngineContainerAndEndpoint(ctx, c.Pipeline("dev-engine"), util.DevEngineOpts{Name: "sdk-nodejs-test"})
	if err != nil {
		return err
	}
	cliBinPath := "/.dagger-cli"

	_, err = nodeJsBase(c).
		WithServiceBinding("dagger-engine", devEngine).
		WithEnvVariable("_EXPERIMENTAL_DAGGER_RUNNER_HOST", endpoint).
		WithMountedFile(cliBinPath, util.DaggerBinary(c)).
		WithEnvVariable("_EXPERIMENTAL_DAGGER_CLI_BIN", cliBinPath).
		WithExec([]string{"yarn", "test"}).
		Sync(ctx)
	return err
}

// Generate re-generates the SDK API
func (t Nodejs) Generate(ctx context.Context) error {
	c, err := dagger.Connect(ctx, dagger.WithLogOutput(os.Stderr))
	if err != nil {
		return err
	}
	defer c.Close()

	c = c.Pipeline("sdk").Pipeline("nodejs").Pipeline("generate")

	devEngine, endpoint, err := util.CIDevEngineContainerAndEndpoint(ctx, c.Pipeline("dev-engine"), util.DevEngineOpts{Name: "sdk-nodejs-generate"})
	if err != nil {
		return err
	}
	cliBinPath := "/.dagger-cli"

	generated, err := nodeJsBase(c).
		WithServiceBinding("dagger-engine", devEngine).
		WithMountedFile("/usr/local/bin/codegen", util.CodegenBinary(c)).
		WithMountedFile(cliBinPath, util.DaggerBinary(c)).
		WithEnvVariable("_EXPERIMENTAL_DAGGER_RUNNER_HOST", endpoint).
		WithEnvVariable("_EXPERIMENTAL_DAGGER_CLI_BIN", cliBinPath).
		WithExec([]string{"codegen", "--lang", "nodejs", "-o", path.Dir(nodejsGeneratedAPIPath)}).
		WithExec([]string{
			"yarn",
			"fmt",
			nodejsGeneratedAPIPath,
		}).
		File(nodejsGeneratedAPIPath).
		Contents(ctx)
	if err != nil {
		return err
	}
	return os.WriteFile(nodejsGeneratedAPIPath, []byte(generated), 0o600)
}

// Publish publishes the Node.js SDK
func (t Nodejs) Publish(ctx context.Context, tag string) error {
	c, err := dagger.Connect(ctx, dagger.WithLogOutput(os.Stderr))
	if err != nil {
		return err
	}
	defer c.Close()

	c = c.Pipeline("sdk").Pipeline("nodejs").Pipeline("publish")

	var (
		version = strings.TrimPrefix(tag, "sdk/nodejs/v")
		token   = os.Getenv("NPM_TOKEN")
	)

	build := nodeJsBase(c).WithExec([]string{"npm", "run", "build"})

	// configure .npmrc
	npmrc := fmt.Sprintf(`//registry.npmjs.org/:_authToken=%s
registry=https://registry.npmjs.org/
always-auth=true`, token)
	if err = os.WriteFile("sdk/nodejs/.npmrc", []byte(npmrc), 0o600); err != nil {
		return err
	}

	// set version & publish
	_, err = build.
		WithExec([]string{"npm", "version", version}).
		WithExec([]string{"npm", "publish", "--access", "public"}).
		Sync(ctx)

	return err
}

// Bump the Node.js SDK's Engine dependency
func (t Nodejs) Bump(_ context.Context, version string) error {
	// trim leading v from version
	version = strings.TrimPrefix(version, "v")

	engineReference := fmt.Sprintf("// Code generated by dagger. DO NOT EDIT.\n"+
		"export const CLI_VERSION = %q\n", version)

	// NOTE: if you change this path, be sure to update .github/workflows/publish.yml so that
	// provision tests run whenever this file changes.
	return os.WriteFile("sdk/nodejs/provisioning/default.ts", []byte(engineReference), 0o600)
}

func nodeJsBase(c *dagger.Client) *dagger.Container {
	appDir := "sdk/nodejs"
	src := c.Directory().WithDirectory("/", util.Repository(c).Directory(appDir))

	// Mirror the same dir structure from the repo because of the
	// relative paths in eslint (for docs linting).
	mountPath := fmt.Sprintf("/%s", appDir)

	return c.Container().
		// ⚠️  Keep this in sync with the engine version defined in package.json
		From("node:16-alpine").
		WithWorkdir(mountPath).
		WithMountedCache("/usr/local/share/.cache/yarn", c.CacheVolume("yarn_cache")).
		WithFile(fmt.Sprintf("%s/package.json", mountPath), src.File("package.json")).
		WithFile(fmt.Sprintf("%s/yarn.lock", mountPath), src.File("yarn.lock")).
		WithExec([]string{"yarn", "install"}).
		WithDirectory(mountPath, src)
}
