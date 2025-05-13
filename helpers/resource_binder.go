// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package helpers

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/hashicorp/go-azure-sdk/sdk/auth"
	"github.com/hashicorp/go-azure-sdk/sdk/environments"
	"github.com/hashicorp/go-cty/cty"
	ctyjson "github.com/hashicorp/go-cty/cty/json"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
	"github.com/hashicorp/terraform-provider-azurerm/internal/clients"
	"github.com/hashicorp/terraform-provider-azurerm/internal/provider"
)

type AzureCertificateCreds struct {
	ClientID              string
	ClientCertificateData []byte
	TenantID              string
}

func ReadResource(resource_type string, id string, creds *AzureCertificateCreds) (any, error) {
	p := provider.AzureProvider()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if creds == nil {
		p.ConfigureContextFunc = func(ctx context.Context, d *schema.ResourceData) (interface{}, diag.Diagnostics) {
			envName := d.Get("environment").(string)
			env, err := environments.FromName(envName)
			if err != nil {
				log.Fatalf("configuring environment %q: %v", envName, err)
			}

			authConfig := &auth.Credentials{
				Environment:                       *env,
				EnableAuthenticatingUsingAzureCLI: true,
				AzureCliSubscriptionIDHint:        d.Get("subscription_id").(string),
			}

			return provider.BuildClient(ctx, p, d, authConfig)
		}
	} else {
		p.ConfigureContextFunc = func(ctx context.Context, d *schema.ResourceData) (interface{}, diag.Diagnostics) {
			envName := d.Get("environment").(string)
			env, err := environments.FromName(envName)
			if err != nil {
				log.Fatalf("configuring environment %q: %v", envName, err)
			}

			authConfig := &auth.Credentials{
				Environment:           *env,
				ClientID:              creds.ClientID,
				ClientCertificateData: creds.ClientCertificateData,
				TenantID:              creds.TenantID,
				EnableAuthenticatingUsingClientCertificate: true,
			}

			return provider.BuildClient(ctx, p, d, authConfig)
		}
	}

	if diags := p.Configure(ctx, terraform.NewResourceConfigRaw(nil)); diags != nil && diags.HasError() {
		log.Fatalf("provider failed to configure: %v", diags)
	}

	log.Printf("Reading %s with ID %s", resource_type, id)

	resource, ok := p.ResourcesMap[resource_type]
	if !ok {
		return nil, fmt.Errorf("resource type %s unsupported", resource_type)
	}

	data := resource.Data(nil)
	data.SetId(id)

	meta := p.Meta().(*clients.Client)

	if data == nil && meta == nil {
		return nil, fmt.Errorf("failed to read resource: data or meta is nil")
	}

	err := resource.Read(data, meta)
	if err != nil {
		return nil, fmt.Errorf("failed to read resource %s: %v", resource_type, err)
	}

	attr_value, err := data.State().AttrsAsObjectValue(resource.CoreConfigSchema().ImpliedType())
	if err != nil {
		return nil, fmt.Errorf("failed to get attributes as object value: %v", err)
	}
	jsondata_something, err := ctyjson.Marshal(attr_value, resource.CoreConfigSchema().ImpliedType())
	if err != nil {
		return nil, fmt.Errorf("failed to marshal data: %v", err)
	}
	log.Printf("SOMETHINGSOMETHING %s with ID %s", id, jsondata_something)

	attr := normalizeTypesOfCtyJson(convertCtyValue(attr_value).(map[string]any), resource.CoreConfigSchema().ImpliedType())
	if attr == nil {
		return nil, fmt.Errorf("failed to convert attributes to map: %v", attr_value)
	}

	return attr, nil
}

func normalizeTypesOfCtyJson(jsonValue map[string]any, jsonType cty.Type) map[string]any {
	for key, val := range jsonValue {
		if val == nil {
			log.Printf("K-V: %s-%s ,Type: %s, IsListType: %s", key, val, jsonType.AttributeType(key), jsonType.AttributeType(key).IsListType())
			if jsonType.AttributeType(key).IsListType() {
				jsonValue[key] = []any{}
			}
		}
	}

	return jsonValue
}

func convertCtyMap(ctyMap map[string]*cty.Value) (map[string]interface{}, error) {
	result := make(map[string]interface{})

	for key, valPtr := range ctyMap {
		if valPtr == nil {
			result[key] = nil
			continue
		} else if valPtr.IsNull() {
			if valPtr.Type().IsListType() {
				result[key] = []any{}
			} else {
				result[key] = nil
			}
			continue
		}

		val := *valPtr // Dereference pointer

		switch {
		case val.Type().IsPrimitiveType():
			// Convert primitive types
			if val.Type().Equals(cty.String) {
				result[key] = val.AsString()
			} else if val.Type().Equals(cty.Number) {
				floatVal, _ := val.AsBigFloat().Float64()
				result[key] = floatVal
			} else if val.Type().Equals(cty.Bool) {
				result[key] = val.True()
			}
		case val.Type().IsListType() || val.Type().IsTupleType() || val.Type().IsSetType():
			// Convert list/tuple to slice
			listVals := val.AsValueSlice()
			listResult := make([]interface{}, len(listVals))
			for i, v := range listVals {
				listResult[i] = convertCtyValue(v)
			}
			result[key] = listResult
		case val.Type().IsMapType() || val.Type().IsObjectType():
			// Convert nested maps/objects
			mapVals := val.AsValueMap()
			convertedMap, err := convertCtyMapPtr(mapVals)
			if err != nil {
				return nil, err
			}
			result[key] = convertedMap
		default:
			return nil, fmt.Errorf("unsupported cty.Value type: %s", val.Type().FriendlyName())
		}
	}

	return result, nil
}

// Convert *cty.Value to interface{} (helper function)
func convertCtyValue(val cty.Value) interface{} {
	if val.IsNull() {
		return nil
	}

	if val.Type().IsPrimitiveType() {
		if val.Type().Equals(cty.String) {
			return val.AsString()
		} else if val.Type().Equals(cty.Number) {
			floatVal, _ := val.AsBigFloat().Float64()
			return floatVal
		} else if val.Type().Equals(cty.Bool) {
			return val.True()
		}
	} else if val.Type().IsListType() || val.Type().IsTupleType() {
		listVals := val.AsValueSlice()
		listResult := make([]interface{}, len(listVals))
		for i, v := range listVals {
			listResult[i] = convertCtyValue(v)
		}
		return listResult
	} else if val.Type().IsMapType() || val.Type().IsObjectType() {
		mapVals := val.AsValueMap()
		convertedMap, _ := convertCtyMapPtr(mapVals)
		return convertedMap
	}

	return nil
}

// Convert map[string]cty.Value to map[string]interface{}
func convertCtyMapPtr(ctyMap map[string]cty.Value) (map[string]interface{}, error) {
	ptrMap := make(map[string]*cty.Value)
	for k, v := range ctyMap {
		val := v
		ptrMap[k] = &val
	}
	return convertCtyMap(ptrMap)
}
