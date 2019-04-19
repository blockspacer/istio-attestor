package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/hashicorp/hcl"
	"github.com/spiffe/spire/pkg/common/catalog"
	spi "github.com/spiffe/spire/proto/spire/common/plugin"
	"github.com/spiffe/spire/proto/spire/server/nodeattestor"
	"github.com/zeebo/errs"
	k8sauth "k8s.io/api/authentication/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	pluginName       = "istio"
	spiffeIdTemplate = "spiffe://%s/ns/%s/sa/%s"
)

var pluginErr = errs.Class("istio")

// IstioAttestorPlugin implements attestation for Istio's agent node
type IstioAttestorPlugin struct {
	// Kubernetes client to verify provided token
	kubeClient *kubernetes.Clientset
	mtx        *sync.Mutex
}

// IstioAttestorConfig holds hcl configurations for Istio attestor plugin
type IstioAttestorConfig struct {
	ConfigPath string `hcl:"k8s_config_path"`
}

// istioAttestedData holds data provided for istio node agent
type istioAttestedData struct {
	Token       string `json:"token"`
	TrustDomain string `json:"trustDomain"`
}

// JWTClaims holds namespace and service account from token verification
type JWTClaims struct {
	Namespace      string
	ServiceAccount string
}

// New create a new Istio attestor plugin with default values
func New() *IstioAttestorPlugin {
	return &IstioAttestorPlugin{
		mtx: &sync.Mutex{},
	}
}

// Attest implements the server side logic to verify provided token using k8s token service
func (i *IstioAttestorPlugin) Attest(stream nodeattestor.NodeAttestor_AttestServer) error {
	var attestedData istioAttestedData

	req, err := stream.Recv()
	if err != nil {
		return pluginErr.Wrap(err)
	}

	// verify request is processed for expected plugin
	if req.AttestationData.Type != pluginName {
		return pluginErr.New("unexpected attestation data type %q", req.AttestationData.Type)
	}

	// extract attested data from Istio
	if err := json.Unmarshal(req.AttestationData.Data, &attestedData); err != nil {
		return pluginErr.New("error parsing message from attestation data", err)
	}

	// remove "Bearer " and validate token using using token service with provided service account.
	token := strings.TrimPrefix(attestedData.Token, "Bearer ")

	// validate token with token review api call using Kubernetes client
	claim, err := i.validateJWT(token)
	if err != nil {
		return pluginErr.New("provided token from request is not valid: ", err)
	}

	return stream.Send(&nodeattestor.AttestResponse{
		Valid:        true,
		BaseSPIFFEID: fmt.Sprintf(spiffeIdTemplate, attestedData.TrustDomain, claim.Namespace, claim.ServiceAccount),
	})
}

// Configure configures the Istio attestor plugin
func (i *IstioAttestorPlugin) Configure(ctx context.Context, req *spi.ConfigureRequest) (*spi.ConfigureResponse, error) {
	config := &IstioAttestorConfig{}

	err := hcl.Decode(&config, req.Configuration)
	if err != nil {
		return nil, pluginErr.New("error parsing Istio Attestor configuration: %s", err)
	}

	kubeClient, err := i.newKubeClient(config.ConfigPath)
	if err != nil {
		return nil, pluginErr.New("error creating kubeClient: %v ", err)
	}

	i.mtx.Lock()
	defer i.mtx.Unlock()
	i.kubeClient = kubeClient

	return &spi.ConfigureResponse{}, nil
}

// GetPluginInfo returns the version and other metadata of the plugin.
func (i *IstioAttestorPlugin) GetPluginInfo(ctx context.Context, request *spi.GetPluginInfoRequest) (*spi.GetPluginInfoResponse, error) {
	return &spi.GetPluginInfoResponse{}, nil
}

// newKubeClient create a Kubernetes client, it is using provided config, in case config is not provided
// a client with in cluster configuration is created
func (i IstioAttestorPlugin) newKubeClient(configPath string) (*kubernetes.Clientset, error) {
	config, err := getConfig(configPath)
	if err != nil {
		return nil, pluginErr.Wrap(err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, pluginErr.Wrap(err)
	}

	return client, nil
}

func getConfig(configPath string) (*rest.Config, error) {
	if configPath != "" {
		return clientcmd.BuildConfigFromFlags("", configPath)
	}

	return rest.InClusterConfig()
}

// getKubeClient provide configured Kubernetes client
func (i *IstioAttestorPlugin) getKubeClient() (*kubernetes.Clientset, error) {
	i.mtx.Lock()
	defer i.mtx.Unlock()

	if i.kubeClient == nil {
		return nil, pluginErr.New("no Kubernetes client is configured")
	}

	return i.kubeClient, nil
}

// validateJwt validate provided jwt using Kubernetes token review api.
func (i *IstioAttestorPlugin) validateJWT(jwt string) (*JWTClaims, error) {
	reviewReq := &k8sauth.TokenReview{
		Spec: k8sauth.TokenReviewSpec{
			Token: jwt,
		},
	}

	kubeClient, err := i.getKubeClient()
	if err != nil {
		return nil, err
	}
	// cal token review api to verify jwt token
	tokenReview, err := kubeClient.AuthenticationV1().TokenReviews().Create(reviewReq)
	if err != nil {
		return nil, pluginErr.New("could not get a token review response: %v", err)
	}

	if tokenReview.Status.Error != "" {
		return nil, pluginErr.New("service account authentication status error: %v", tokenReview.Status.Error)
	}

	if !tokenReview.Status.Authenticated {
		return nil, pluginErr.New("token is not authenticated")
	}

	// verify if use is in service account group
	inServiceAccountGroup := false
	for _, group := range tokenReview.Status.User.Groups {
		if group == "system:serviceaccounts" {
			inServiceAccountGroup = true
			break
		}
	}
	if !inServiceAccountGroup {
		return nil, pluginErr.New("the token is not a service account")
	}

	// username format: system:serviceaccount:(NAMESPACE):(SERVICEACCOUNT)
	subStrings := strings.Split(tokenReview.Status.User.Username, ":")
	if len(subStrings) != 4 {
		return nil, pluginErr.New("token review returned an invalid username field")
	}

	return &JWTClaims{Namespace: subStrings[2], ServiceAccount: subStrings[3]}, nil
}

func main() {
	catalog.PluginMain(
		catalog.MakePlugin(pluginName, nodeattestor.PluginServer(New())),
	)
}
