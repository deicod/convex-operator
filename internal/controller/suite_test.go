/*
Copyright (C) 2025 Darko Luketic <info@icod.de>

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.
*/

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	convexv1alpha1 "github.com/deicod/convex-operator/api/v1alpha1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	// +kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	ctx       context.Context
	cancel    context.CancelFunc
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client
)

const (
	defaultGatewayAPIModuleVersion = "v1.4.1"
	gatewayAPICRDVersionEnvVar     = "GATEWAY_API_CRD_VERSION"
	gatewayAPICRDPathEnvVar        = "GATEWAY_API_CRD_PATH"
)

func gatewayAPIModuleVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return defaultGatewayAPIModuleVersion
	}
	for _, dep := range info.Deps {
		if dep.Path == "sigs.k8s.io/gateway-api" && dep.Version != "" {
			return dep.Version
		}
	}
	return defaultGatewayAPIModuleVersion
}

func gatewayAPICRDVersion() string {
	if version := strings.TrimSpace(os.Getenv(gatewayAPICRDVersionEnvVar)); version != "" {
		return version
	}
	return gatewayAPIModuleVersion()
}

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	var err error
	err = convexv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())
	Expect(gatewayv1.Install(scheme.Scheme)).To(Succeed())

	// +kubebuilder:scaffold:scheme

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	if gatewayCRDs := gatewayStandardCRDPath(); gatewayCRDs != "" {
		testEnv.CRDDirectoryPaths = append(testEnv.CRDDirectoryPaths, gatewayCRDs)
	}

	// Retrieve the first found binary directory to allow running tests from IDEs
	if getFirstFoundEnvTestBinaryDir() != "" {
		testEnv.BinaryAssetsDirectory = getFirstFoundEnvTestBinaryDir()
	}

	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

// getFirstFoundEnvTestBinaryDir locates the first binary in the specified path.
// ENVTEST-based tests depend on specific binaries, usually located in paths set by
// controller-runtime. When running tests directly (e.g., via an IDE) without using
// Makefile targets, the 'BinaryAssetsDirectory' must be explicitly configured.
//
// This function streamlines the process by finding the required binaries, similar to
// setting the 'KUBEBUILDER_ASSETS' environment variable. To ensure the binaries are
// properly set up, run 'make setup-envtest' beforehand.
func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		logf.Log.Error(err, "Failed to read directory", "path", basePath)
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}

// gatewayStandardCRDPath resolves the path to the Gateway API standard CRDs in the module cache.
// Tests depend on these CRDs to install gateway.networking.k8s.io types.
func gatewayStandardCRDPath() string {
	if override := strings.TrimSpace(os.Getenv(gatewayAPICRDPathEnvVar)); override != "" {
		if _, err := os.Stat(override); err != nil {
			logf.Log.Error(err, "Gateway API CRD path override not found", "path", override)
			return ""
		}
		return override
	}

	version := gatewayAPICRDVersion()
	moduleDir, err := gatewayAPIModuleDir(version)
	if err != nil {
		logf.Log.Error(err, "Failed to resolve Gateway API module path", "version", version)
		return ""
	}

	path := filepath.Join(moduleDir, "config", "crd", "standard")
	if _, err := os.Stat(path); err != nil {
		logf.Log.Error(err, "Gateway API CRDs not found", "path", path, "version", version)
		return ""
	}
	return path
}

func gatewayAPIModuleDir(version string) (string, error) {
	modCache := os.Getenv("GOMODCACHE")
	if modCache == "" {
		output, err := exec.Command("go", "env", "GOMODCACHE").Output()
		if err != nil {
			return "", fmt.Errorf("resolve GOMODCACHE: %w", err)
		}
		modCache = strings.TrimSpace(string(output))
	}

	moduleDir := filepath.Join(modCache, "sigs.k8s.io", fmt.Sprintf("gateway-api@%s", version))
	if _, err := os.Stat(moduleDir); err == nil {
		return moduleDir, nil
	}

	output, err := exec.Command("go", "mod", "download", "-json", fmt.Sprintf("sigs.k8s.io/gateway-api@%s", version)).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("download Gateway API %s: %w: %s", version, err, strings.TrimSpace(string(output)))
	}

	var module struct {
		Dir string
	}
	if err := json.Unmarshal(output, &module); err != nil {
		return "", fmt.Errorf("parse go mod download output for Gateway API %s: %w", version, err)
	}
	if module.Dir == "" {
		return "", fmt.Errorf("Gateway API %s download did not report a module dir", version)
	}
	return module.Dir, nil
}
