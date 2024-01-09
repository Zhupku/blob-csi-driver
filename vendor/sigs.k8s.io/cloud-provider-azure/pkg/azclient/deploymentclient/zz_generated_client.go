// /*
// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// */

// Code generated by client-gen. DO NOT EDIT.
package deploymentclient

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/tracing"
	resources "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"

	"sigs.k8s.io/cloud-provider-azure/pkg/azclient/utils"
)

type Client struct {
	*resources.DeploymentsClient
	subscriptionID string
	tracer         tracing.Tracer
}

func New(subscriptionID string, credential azcore.TokenCredential, options *arm.ClientOptions) (Interface, error) {
	if options == nil {
		options = utils.GetDefaultOption()
	}
	tr := options.TracingProvider.NewTracer(utils.ModuleName, utils.ModuleVersion)

	client, err := resources.NewDeploymentsClient(subscriptionID, credential, options)
	if err != nil {
		return nil, err
	}
	return &Client{
		DeploymentsClient: client,
		subscriptionID:    subscriptionID,
		tracer:            tr,
	}, nil
}

const DeleteOperationName = "DeploymentsClient.Delete"

// Delete deletes a Deployment by name.
func (client *Client) Delete(ctx context.Context, resourceGroupName string, resourceName string) (err error) {
	ctx = utils.ContextWithClientName(ctx, "DeploymentsClient")
	ctx = utils.ContextWithRequestMethod(ctx, "Delete")
	ctx = utils.ContextWithResourceGroupName(ctx, resourceGroupName)
	ctx = utils.ContextWithSubscriptionID(ctx, client.subscriptionID)
	ctx, endSpan := runtime.StartSpan(ctx, DeleteOperationName, client.tracer, nil)
	defer endSpan(err)
	_, err = utils.NewPollerWrapper(client.BeginDelete(ctx, resourceGroupName, resourceName, nil)).WaitforPollerResp(ctx)
	return err
}
