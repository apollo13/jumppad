package jumppad

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"

	"github.com/jumppad-labs/hclconfig"
	"github.com/jumppad-labs/hclconfig/types"
	"github.com/jumppad-labs/jumppad/pkg/clients/logger"
	"github.com/jumppad-labs/jumppad/pkg/config"
	"github.com/jumppad-labs/jumppad/pkg/config/mocks"
	"github.com/jumppad-labs/jumppad/pkg/config/resources/cache"
	"github.com/jumppad-labs/jumppad/pkg/config/resources/container"
	"github.com/jumppad-labs/jumppad/pkg/jumppad/constants"
	"github.com/jumppad-labs/jumppad/pkg/utils"
	"github.com/jumppad-labs/jumppad/testutils"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

var lock = sync.Mutex{}

func setupTests(t *testing.T, returnVals map[string]error) (*EngineImpl, *mocks.Providers) {
	return setupTestsBase(t, returnVals, "")
}

func setupTestsWithState(t *testing.T, returnVals map[string]error, state string) (*EngineImpl, *mocks.Providers) {
	return setupTestsBase(t, returnVals, state)
}

func setupTestsBase(t *testing.T, returnVals map[string]error, state string) (*EngineImpl, *mocks.Providers) {
	l := logger.NewTestLogger(t)

	log.SetOutput(l.StandardWriter())

	pm := mocks.NewProviders(returnVals)
	pm.On("GetProvider", mock.Anything)

	e := &EngineImpl{
		log:       l,
		providers: pm,
	}

	testutils.SetupState(t, state)

	return e, pm
}

func testLoadState(t *testing.T, e *EngineImpl) *hclconfig.Config {
	c, err := config.LoadState()
	require.NoError(t, err)

	return c
}

func getResourceFromMock(mp *mocks.Providers, index int) types.Resource {
	p := mp.Providers[index]
	r := testutils.GetCalls(&p.Mock, "Init")[0].Arguments[0]

	return r.(types.Resource)
}

func getMetaFromMock(mp *mocks.Providers, index int) types.ResourceMetadata {
	return *getResourceFromMock(mp, index).Metadata()
}

func TestApplyWithSingleFile(t *testing.T) {
	e, mp := setupTests(t, nil)

	_, err := e.Apply("../../examples/single_file/container.hcl")
	require.NoError(t, err)

	require.Len(t, e.config.Resources, 7) // 6 resources in the file plus the image cache

	// Because of the DAG both network and template are top level resources
	require.ElementsMatch(t,
		[]string{
			"default",
			"onprem",
			"default",
			"consul_config",
		},
		[]string{
			getMetaFromMock(mp, 0).Name,
			getMetaFromMock(mp, 1).Name,
			getMetaFromMock(mp, 2).Name,
			getMetaFromMock(mp, 3).Name,
		},
	)

	require.Equal(t, "consul", getMetaFromMock(mp, 4).Name)
	require.Equal(t, "consul_addr", getMetaFromMock(mp, 5).Name)
}

func TestApplyAddsImageCache(t *testing.T) {
	e, _ := setupTests(t, nil)

	_, err := e.Apply("../../examples/single_file/container.hcl")
	require.NoError(t, err)

	dc := e.ResourceCountForType(cache.TypeImageCache)
	require.Equal(t, 1, dc)
}

func TestApplyWithSingleFileAndVariables(t *testing.T) {
	e, mp := setupTests(t, nil)

	_, err := e.ApplyWithVariables("../../examples/single_file/container.hcl", nil, "../../examples/single_file/default.vars")
	require.NoError(t, err)
	require.Len(t, e.config.Resources, 7) // 6 resources in the file plus the image cache

	// then the container should be created
	require.Equal(t, "consul", getMetaFromMock(mp, 4).Name)

	// finally the provider for the image cache should be updated
	require.Equal(t, "consul_addr", getMetaFromMock(mp, 5).Name)

	// check the variable has overridden the image
	cont := getResourceFromMock(mp, 4).(*container.Container)
	require.Equal(t, "consul:1.8.1", cont.Image.Name)
}

func TestApplyCallsProviderCreateForEachProvider(t *testing.T) {
	e, mp := setupTests(t, nil)

	_, err := e.Apply("../../examples/single_k3s_cluster")
	require.NoError(t, err)

	// should have call create for each resource in the config
	// and once for ImageCache that is manually added.
	// for every network that is created refresh is called on the image cache
	rc := len(e.config.Resources)
	testAssertMethodCalled(t, mp, "Create", rc)
	testAssertMethodCalled(t, mp, "Refresh", 1)

	// the state should also contain 11 resources
	sf := testLoadState(t, e)
	require.Equal(t, 11, sf.ResourceCount())
}

func TestApplyDoesNotCallsProviderCreateWhenInState(t *testing.T) {
	e, mp := setupTestsWithState(t, nil, existingState)

	_, err := e.Apply("../../examples/single_file")
	require.NoError(t, err)

	// should only be called for resources that are not in the state
	testAssertMethodCalled(t, mp, "Create", 3)

	// the state should also contain 7 resources
	sf := testLoadState(t, e)
	require.Equal(t, 7, sf.ResourceCount())
}

func TestApplyRemovesItemsInStateWhenNotInFiles(t *testing.T) {
	e, mp := setupTestsWithState(t, nil, existingState)

	_, err := e.Apply("../../examples/single_file")
	require.NoError(t, err)

	// should only be called for resources that are not in the state
	testAssertMethodCalled(t, mp, "Create", 3)

	// should remove items not in files
	testAssertMethodCalled(t, mp, "Destroy", 2)

	// the state should also contain 7 resources
	sf := testLoadState(t, e)
	require.Equal(t, 7, sf.ResourceCount())
}

func TestApplyNotCallsProviderCreateForDisabledResources(t *testing.T) {
	e, mp := setupTests(t, nil)

	// contains 3 resources one is disabled
	_, err := e.Apply("../../examples/disabled")
	require.NoError(t, err)

	// should have call create for non disabled resources
	testAssertMethodCalled(t, mp, "Create", 3) // ImageCache is always created

	// disabled resources should still be added to the state
	sf := testLoadState(t, e)

	// should contain 3 from the config plus the image cache
	require.Equal(t, 4, sf.ResourceCount())

	// the resource should be in the state but there should be no status
	r, err := sf.FindResource("resource.container.consul_disabled")
	require.NoError(t, err)
	require.Nil(t, r.Metadata().Properties[constants.PropertyStatus])
}

func TestApplyShouldNotAddDuplicateDisabledResources(t *testing.T) {
	e, mp := setupTestsWithState(t, nil, disabledState)

	// contains 2 resources one is disabled
	_, err := e.Apply("../../examples/disabled")
	require.NoError(t, err)

	// should have call create for each provider
	testAssertMethodCalled(t, mp, "Create", 1) // ImageCache is always created

	// disabled resources should still be added to the state
	sf := testLoadState(t, e)

	// should contain 3 from the config plus the image cache
	// should not duplicate the disabled container as this already exists in the
	// state
	require.Equal(t, 4, sf.ResourceCount())

	// the status should be set to disabled
	r, err := sf.FindResource("resource.container.consul_disabled")
	require.NoError(t, err)
	require.Equal(t, constants.StatusDisabled, r.Metadata().Properties[constants.PropertyStatus])
}

func TestApplySetsCreatedStatusForEachResource(t *testing.T) {
	e, mp := setupTests(t, nil)

	_, err := e.Apply("../../examples/single_file/container.hcl")
	require.NoError(t, err)

	require.Equal(t, 7, e.config.ResourceCount())

	// should only call create and destroy for the cache as this is pending update
	testAssertMethodCalled(t, mp, "Create", 5) // ImageCache is always created

	sf := testLoadState(t, e)

	r, err := sf.FindResource("resource.container.consul")
	require.NoError(t, err)
	require.Equal(t, constants.StatusCreated, r.Metadata().Properties[constants.PropertyStatus])

	r, err = sf.FindResource("resource.network.onprem")
	require.NoError(t, err)
	require.Equal(t, constants.StatusCreated, r.Metadata().Properties[constants.PropertyStatus])

	r, err = sf.FindResource("resource.template.consul_config")
	require.NoError(t, err)
	require.Equal(t, constants.StatusCreated, r.Metadata().Properties[constants.PropertyStatus])
}

func TestApplyCallsProviderGenerateErrorStopsExecution(t *testing.T) {
	e, mp := setupTests(t, map[string]error{"onprem": fmt.Errorf("boom")})

	_, err := e.Apply("../../examples/single_file/container.hcl")
	require.Error(t, err)

	// should have call create for each provider
	// there are two top level config items, template and network
	// network will fail
	// ImageCache should always be created
	testAssertMethodCalled(t, mp, "Create", 3)

	sf := testLoadState(t, e)

	// should set failed status for network
	r, err := sf.FindResource("resource.network.onprem")
	require.NoError(t, err)
	require.Equal(t, constants.StatusFailed, r.Metadata().Properties[constants.PropertyStatus])

	// should set created status for template
	r, err = sf.FindResource("resource.template.consul_config")
	require.NoError(t, err)
	require.Equal(t, constants.StatusCreated, r.Metadata().Properties[constants.PropertyStatus])

	// container should not be in the state
	_, err = sf.FindResource("resource.container.consul")
	require.Error(t, err)
}

func TestApplyCallsProviderDestroyAndCreateForFailedResources(t *testing.T) {
	e, mp := setupTestsWithState(t, nil, failedState)

	_, err := e.Apply("../../examples/single_file/container.hcl")
	require.NoError(t, err)

	// should have call create for each provider
	testAssertMethodCalled(t, mp, "Destroy", 1)
	testAssertMethodCalled(t, mp, "Create", 5) // ImageCache is always created
}

func TestApplyCallsProviderDestroyForTaintedResources(t *testing.T) {
	e, mp := setupTestsWithState(t, nil, taintedState)

	_, err := e.Apply("../../examples/single_file/container.hcl")
	require.NoError(t, err)

	// should have call create for each provider
	testAssertMethodCalled(t, mp, "Destroy", 1)
	testAssertMethodCalled(t, mp, "Create", 5) // ImageCache is always created
}

func TestApplyCallsProviderDestroyForDisabledResources(t *testing.T) {
	// resources that were created but subsequently set to disabled should be
	// destroyed
	e, mp := setupTestsWithState(t, nil, disabledAndCreatedState)

	_, err := e.Apply("../../examples/disabled/config.hcl")
	require.NoError(t, err)

	// get the disabled resource
	r, err := e.config.FindResource("resource.container.consul_disabled")
	require.NoError(t, err)
	require.NotNil(t, r)

	// property should be disabled
	require.Equal(t, constants.StatusDisabled, r.Metadata().Properties[constants.PropertyStatus])

	// should have call create for each provider
	testAssertMethodCalled(t, mp, "Destroy", 1, r)
	testAssertMethodCalled(t, mp, "Create", 0) // ImageCache is always created
}

func TestApplyCallsProviderRefreshForCreatedResources(t *testing.T) {
	e, mp := setupTestsWithState(t, nil, existingState)

	_, err := e.Apply("../../examples/single_file/container.hcl")
	require.NoError(t, err)

	// should only call twice as there is only one item in the state
	// that is in the config
	// should also be called for the image cache as there is a new network
	testAssertMethodCalled(t, mp, "Refresh", 2)
}

func TestApplyCallsProviderRefreshWithErrorHaltsExecution(t *testing.T) {
	e, mp := setupTestsWithState(t, map[string]error{"consul_config": fmt.Errorf("boom")}, singleFileState)

	_, err := e.Apply("../../examples/single_file/container.hcl")
	require.Error(t, err)

	testAssertMethodCalled(t, mp, "Refresh", 3)
	testAssertMethodCalled(t, mp, "Create", 0)
}

func TestDestroyCallsProviderDestroyForEachProvider(t *testing.T) {
	e, mp := setupTestsWithState(t, nil, existingState)

	err := e.Destroy()
	require.NoError(t, err)

	// should have call create for each provider
	// and once for the image cache
	testAssertMethodCalled(t, mp, "Destroy", 4)

	// state should be removed
	require.NoFileExists(t, utils.StatePath())
}

func TestDestroyNotCallsProviderDestroyForResourcesDisabled(t *testing.T) {
	e, mp := setupTestsWithState(t, nil, disabledState)

	err := e.Destroy()
	require.NoError(t, err)

	// should have call create for each provider
	testAssertMethodCalled(t, mp, "Destroy", 2)
	testAssertMethodCalled(t, mp, "Create", 0) // ImageCache is always created

	// state should be removed
	require.NoFileExists(t, utils.StatePath())
}

func TestDestroyCallsProviderGenerateErrorStopsExecution(t *testing.T) {
	e, mp := setupTestsWithState(t, map[string]error{"mycontainer": fmt.Errorf("boom")}, complexState)

	err := e.Destroy()
	require.Error(t, err)

	// should have call destroy for each provider
	testAssertMethodCalled(t, mp, "Destroy", 2)

	// state should not be removed
	require.FileExists(t, utils.StatePath())
}

func TestDestroyFailSetsStatus(t *testing.T) {
	e, _ := setupTestsWithState(t, map[string]error{"mycontainer": fmt.Errorf("boom")}, complexState)

	err := e.Destroy()
	require.Error(t, err)

	r, _ := e.config.FindResource("resource.container.mycontainer")
	require.Equal(t, constants.StatusFailed, r.Metadata().Properties[constants.PropertyStatus])
}

func TestParseConfig(t *testing.T) {
	e, mp := setupTests(t, nil)

	r, err := e.ParseConfig("../../examples/single_file/container.hcl")
	require.NoError(t, err)

	require.Len(t, r.Resources, 6)

	// should not have crated any providers
	testAssertMethodCalled(t, mp, "Create", 0)
	testAssertMethodCalled(t, mp, "Destroy", 0)
}

func TestParseWithVariables(t *testing.T) {
	e, mp := setupTests(t, nil)

	r, err := e.ParseConfigWithVariables("../../examples/single_file/container.hcl", nil, "../../examples/single_file/default.vars")
	require.NoError(t, err)

	require.Len(t, r.Resources, 6)

	// should not have crated any providers
	testAssertMethodCalled(t, mp, "Create", 0)
	testAssertMethodCalled(t, mp, "Destroy", 0)

	c, err := e.config.FindResource("resource.container.consul")
	require.NoError(t, err)
	require.Equal(t, "consul:1.8.1", c.(*container.Container).Image.Name)
}

func testAssertMethodCalled(t *testing.T, p *mocks.Providers, method string, n int, resource ...types.Resource) {
	if len(resource) > 1 {
		panic("testAssertMethodCalled only expects 0 or 1 resources")
	}

	callCount := 0

	calls := map[string][]string{}

	for i, pm := range p.Providers {
		r := getResourceFromMock(p, i)
		// are we trying to filter on a specific resources
		if len(resource) == 1 {
			if resource[0] != r {
				continue
			}
		}

		for _, c := range pm.Calls {
			calls[r.Metadata().Name] = append(calls[r.Metadata().Name], c.Method)

			if c.Method == method {
				callCount++
			}
		}
	}

	callStrings := []string{}
	for k, v := range calls {
		calls := strings.Join(v, ",")
		callStrings = append(callStrings, fmt.Sprintf("%s[%s]", k, calls))
	}

	callString := strings.Join(callStrings, " ")

	require.Equal(t, n, callCount, fmt.Sprintf("expected %d calls, actual calls %d: %s", n, callCount, callString))
}

var failedState = `
{
  "resources": [
	{
      "name": "onprem",
 	    "properties": {
				"status": "failed"
			},
      "subnet": "10.15.0.0/16",
      "type": "network"
	}
  ]
}
`

var taintedState = `
{
  "resources": [
	{
      "name": "onprem",
 	    "properties": {
				"status": "tainted"
			},
      "subnet": "10.15.0.0/16",
      "type": "network"
	}
  ]
}
`

var existingState = `
{
  "resources": [
	{
      "name": "cloud",
      "properties": {
				"status": "created"
			},
      "subnet": "10.15.0.0/16",
      "type": "network"
	},
	{
      "name": "default",
      "properties": {
				"status": "created"
			},
      "type": "image_cache"
	},
	{
      "name": "container",
      "properties": {
				"status": "created"
			},
			"image": {
				"name": "test"
			},
      "type": "container"
	},
	{
      "name": "consul_config",
      "properties": {
				"status": "created"
			},
      "type": "template"
	}
  ]
}
`

var singleFileState = `
{
  "resources": [
	{
      "name": "onprem",
      "properties": {
				"status": "created"
			},
      "subnet": "10.15.0.0/16",
      "type": "network"
	},
	{
      "name": "default",
      "properties": {
				"status": "created"
			},
      "type": "image_cache"
	},
	{
      "name": "consul",
      "properties": {
				"status": "created"
			},
      "type": "container",
			"image": {
				"name": "test"
			}
	},
	{
      "name": "consul_config",
      "properties": {
				"status": "created"
			},
      "type": "template"
	},
	{
      "name": "consul_addr",
      "properties": {
				"status": "created"
			},
      "type": "output"
	}
  ]
}
`

var complexState = `
{
  "resources": [
	{
      "name": "cloud",
      "subnet": "10.15.0.0/16",
      "properties": {
				"status": "created"
			},
      "type": "network"
	},
	{
      "name": "default",
      "type": "image_cache",
      "properties": {
				"status": "created"
			},
			"depends_on": ["resource.network.cloud"]
	},
	{
      "name": "mytemplate",
      "properties": {
				"status": "created"
			},
      "type": "template"
	},
	{
      "name": "mycontainer",
      "type": "container",
      "properties": {
				"status": "created"
			},
			"depends_on": ["resource.network.cloud", "resource.template.mytemplate"]
	}
  ]
}
`

var disabledState = `
{
  "blueprint": null,
  "resources": [
	{
      "name": "default",
      "status": "created",
      "type": "image_cache"
	},
	{
      "name": "dc1_enabled",
      "properties": {
				"status": "created"
			},
      "subnet": "10.15.0.0/16",
      "type": "network"
	},
	{
		 "disabled": true,
      "name": "consul_disabled",
      "properties": {
				"status": "disabled"
			},
      "type": "container"
	}
  ]
}
`

var disabledAndCreatedState = `
{
  "blueprint": null,
  "resources": [
	{
      "name": "default",
      "properties": {
				"status": "created"
			},
      "type": "image_cache"
	},
	{
      "name": "dc1_enabled",
      "properties": {
				"status": "created"
			},
      "subnet": "10.15.0.0/16",
      "type": "network"
	},
	{
		 "disabled": false,
      "name": "consul_disabled",
      "properties": {
				"status": "created"
			},
      "type": "container",
			"image": {
				"name": "test"
			}	
	},
	{
		 "disabled": false,
      "name": "consul_enabled",
      "properties": {
				"status": "created"
			},
      "type": "container",
			"image": {
				"name": "test"
			}
	}
  ]
}
`
