/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package blob

import (
	"errors"
	"fmt"
	"os"
	"strings"

	kv "github.com/Azure/azure-sdk-for-go/services/keyvault/2016-10-01/keyvault"
	"github.com/Azure/azure-sdk-for-go/services/network/mgmt/2022-07-01/network"
	"github.com/Azure/azure-sdk-for-go/storage"
	"github.com/Azure/go-autorest/autorest"
	"golang.org/x/net/context"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/cloud-provider-azure/pkg/azclient/configloader"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
	providerconfig "sigs.k8s.io/cloud-provider-azure/pkg/provider/config"
)

var (
	DefaultAzureCredentialFileEnv = "AZURE_CREDENTIAL_FILE"
	DefaultCredFilePath           = "/etc/kubernetes/azure.json"
	storageService                = "Microsoft.Storage"
)

// IsAzureStackCloud decides whether the driver is running on Azure Stack Cloud.
func IsAzureStackCloud(cloud *azure.Cloud) bool {
	return !cloud.Config.DisableAzureStackCloud && strings.EqualFold(cloud.Config.Cloud, "AZURESTACKCLOUD")
}

// getCloudProvider get Azure Cloud Provider
func GetCloudProvider(ctx context.Context, kubeconfig, nodeID, secretName, secretNamespace, userAgent string, allowEmptyCloudConfig bool, kubeAPIQPS float64, kubeAPIBurst int) (*azure.Cloud, error) {
	var (
		config     *azure.Config
		kubeClient *clientset.Clientset
		fromSecret bool
	)

	az := &azure.Cloud{}
	az.Environment.StorageEndpointSuffix = storage.DefaultBaseURL

	kubeCfg, err := getKubeConfig(kubeconfig)
	if err == nil && kubeCfg != nil {
		// set QPS and QPS Burst as higher values
		klog.V(2).Infof("set QPS(%f) and QPS Burst(%d) for driver kubeClient", float32(kubeAPIQPS), kubeAPIBurst)
		kubeCfg.QPS = float32(kubeAPIQPS)
		kubeCfg.Burst = kubeAPIBurst
		if kubeClient, err = clientset.NewForConfig(kubeCfg); err != nil {
			klog.Warningf("NewForConfig failed with error: %v", err)
		}
	} else {
		klog.Warningf("get kubeconfig(%s) failed with error: %v", kubeconfig, err)
		if !os.IsNotExist(err) && !errors.Is(err, rest.ErrNotInCluster) {
			return az, fmt.Errorf("failed to get KubeClient: %w", err)
		}
	}

	if kubeClient != nil {
		az.KubeClient = kubeClient
		klog.V(2).Infof("reading cloud config from secret %s/%s", secretNamespace, secretName)
		config, err := configloader.Load[azure.Config](ctx, &configloader.K8sSecretLoaderConfig{
			K8sSecretConfig: configloader.K8sSecretConfig{
				SecretName:      secretName,
				SecretNamespace: secretNamespace,
				CloudConfigKey:  "cloud-config",
			},
			KubeClient: kubeClient,
		}, nil)
		if err == nil && config != nil {
			fromSecret = true
		}
		if err != nil {
			klog.V(2).Infof("InitializeCloudFromSecret: failed to get cloud config from secret %s/%s: %v", secretNamespace, secretName, err)
		}
	}

	if config == nil {
		klog.V(2).Infof("could not read cloud config from secret %s/%s", secretNamespace, secretName)
		credFile, ok := os.LookupEnv(DefaultAzureCredentialFileEnv)
		if ok && strings.TrimSpace(credFile) != "" {
			klog.V(2).Infof("%s env var set as %v", DefaultAzureCredentialFileEnv, credFile)
		} else {
			credFile = DefaultCredFilePath
			klog.V(2).Infof("use default %s env var: %v", DefaultAzureCredentialFileEnv, credFile)
		}

		config, err = configloader.Load[azure.Config](ctx, nil, &configloader.FileLoaderConfig{
			FilePath: credFile,
		})
		if err != nil {
			klog.Warningf("load azure config from file(%s) failed with %v", credFile, err)
		}
	}

	if config == nil {
		if allowEmptyCloudConfig {
			klog.V(2).Infof("no cloud config provided, error: %v, driver will run without cloud config", err)
		} else {
			return az, fmt.Errorf("no cloud config provided, error: %w", err)
		}
	} else {
		config.UserAgent = userAgent
		config.CloudProviderBackoff = true
		if err = az.InitializeCloudFromConfig(context.TODO(), config, fromSecret, false); err != nil {
			klog.Warningf("InitializeCloudFromConfig failed with error: %v", err)
		}
	}

	// reassign kubeClient
	if kubeClient != nil && az.KubeClient == nil {
		az.KubeClient = kubeClient
	}

	isController := (nodeID == "")
	if isController {
		if err == nil {
			// Disable UseInstanceMetadata for controller to mitigate a timeout issue using IMDS
			// https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/168
			klog.V(2).Infof("disable UseInstanceMetadata for controller server")
			az.Config.UseInstanceMetadata = false
		}
		klog.V(2).Infof("starting controller server...")
	} else {
		klog.V(2).Infof("starting node server on node(%s)", nodeID)
	}

	if az.Environment.StorageEndpointSuffix == "" {
		az.Environment.StorageEndpointSuffix = storage.DefaultBaseURL
	}
	return az, nil
}

// getKeyVaultSecretContent get content of the keyvault secret
func (d *Driver) getKeyVaultSecretContent(ctx context.Context, vaultURL string, secretName string, secretVersion string) (content string, err error) {
	kvClient, err := d.initializeKvClient()
	if err != nil {
		return "", fmt.Errorf("failed to get keyvaultClient: %w", err)
	}

	klog.V(2).Infof("get secret from vaultURL(%v), sercretName(%v), secretVersion(%v)", vaultURL, secretName, secretVersion)
	secret, err := kvClient.GetSecret(ctx, vaultURL, secretName, secretVersion)
	if err != nil {
		return "", fmt.Errorf("get secret from vaultURL(%v), sercretName(%v), secretVersion(%v) failed with error: %w", vaultURL, secretName, secretVersion, err)
	}
	return *secret.Value, nil
}

func (d *Driver) initializeKvClient() (*kv.BaseClient, error) {
	kvClient := kv.New()
	token, err := d.getKeyvaultToken()
	if err != nil {
		return nil, err
	}

	kvClient.Authorizer = token
	return &kvClient, nil
}

// getKeyvaultToken retrieves a new service principal token to access keyvault
func (d *Driver) getKeyvaultToken() (authorizer autorest.Authorizer, err error) {
	env := d.cloud.Environment
	kvEndPoint := strings.TrimSuffix(env.KeyVaultEndpoint, "/")
	servicePrincipalToken, err := providerconfig.GetServicePrincipalToken(&d.cloud.Config.AzureAuthConfig, &env, kvEndPoint)
	if err != nil {
		return nil, err
	}
	authorizer = autorest.NewBearerAuthorizer(servicePrincipalToken)
	return authorizer, nil
}

func (d *Driver) updateSubnetServiceEndpoints(ctx context.Context, vnetResourceGroup, vnetName, subnetName string) error {
	if d.cloud.SubnetsClient == nil {
		return fmt.Errorf("SubnetsClient is nil")
	}

	if vnetResourceGroup == "" {
		vnetResourceGroup = d.cloud.ResourceGroup
		if len(d.cloud.VnetResourceGroup) > 0 {
			vnetResourceGroup = d.cloud.VnetResourceGroup
		}
	}

	location := d.cloud.Location
	if vnetName == "" {
		vnetName = d.cloud.VnetName
	}
	if subnetName == "" {
		subnetName = d.cloud.SubnetName
	}

	klog.V(2).Infof("updateSubnetServiceEndpoints on vnetName: %s, subnetName: %s, location: %s", vnetName, subnetName, location)
	if subnetName == "" || vnetName == "" || location == "" {
		return fmt.Errorf("value of subnetName, vnetName or location is empty")
	}

	lockKey := vnetResourceGroup + vnetName + subnetName
	d.subnetLockMap.LockEntry(lockKey)
	defer d.subnetLockMap.UnlockEntry(lockKey)

	subnet, err := d.cloud.SubnetsClient.Get(ctx, vnetResourceGroup, vnetName, subnetName, "")
	if err != nil {
		return fmt.Errorf("failed to get the subnet %s under vnet %s: %v", subnetName, vnetName, err)
	}
	endpointLocaions := []string{location}
	storageServiceEndpoint := network.ServiceEndpointPropertiesFormat{
		Service:   &storageService,
		Locations: &endpointLocaions,
	}
	storageServiceExists := false
	if subnet.SubnetPropertiesFormat == nil {
		subnet.SubnetPropertiesFormat = &network.SubnetPropertiesFormat{}
	}
	if subnet.SubnetPropertiesFormat.ServiceEndpoints == nil {
		subnet.SubnetPropertiesFormat.ServiceEndpoints = &[]network.ServiceEndpointPropertiesFormat{}
	}
	serviceEndpoints := *subnet.SubnetPropertiesFormat.ServiceEndpoints
	for _, v := range serviceEndpoints {
		if strings.HasPrefix(pointer.StringDeref(v.Service, ""), storageService) {
			storageServiceExists = true
			klog.V(4).Infof("serviceEndpoint(%s) is already in subnet(%s)", storageService, subnetName)
			break
		}
	}

	if !storageServiceExists {
		serviceEndpoints = append(serviceEndpoints, storageServiceEndpoint)
		subnet.SubnetPropertiesFormat.ServiceEndpoints = &serviceEndpoints

		klog.V(2).Infof("begin to update the subnet %s under vnet %s rg %s", subnetName, vnetName, vnetResourceGroup)
		if err := d.cloud.SubnetsClient.CreateOrUpdate(ctx, vnetResourceGroup, vnetName, subnetName, subnet); err != nil {
			return fmt.Errorf("failed to update the subnet %s under vnet %s: %v", subnetName, vnetName, err)
		}
		klog.V(2).Infof("serviceEndpoint(%s) is appended in subnet(%s)", storageService, subnetName)
	}

	return nil
}

func getKubeConfig(kubeconfig string) (config *rest.Config, err error) {
	if kubeconfig != "" {
		if config, err = clientcmd.BuildConfigFromFlags("", kubeconfig); err != nil {
			return nil, err
		}
	} else {
		if config, err = rest.InClusterConfig(); err != nil {
			return nil, err
		}
	}
	return config, err
}
