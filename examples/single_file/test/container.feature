Feature: Docker Container
  In order to test Shipyard creates containers correctly
  I should apply a blueprint which defines a simple container setup
  and test the resources are created correctly

Scenario: Single Container from Local Blueprint
  Given I have a running blueprint
  Then the following resources should be running
    | name                      | type      |
    | onprem                    | network   |
    | consul                    | container |
  And the info "{.HostConfig.PortBindings['8500/'][0].HostPort}" for the running "container" called "consul" should equal "8500"
  And the info "{.HostConfig.PortBindings['8500/'][0].HostPort}" for the running "container" called "consul" should contain "85"
  And the info "{.HostConfig.PortBindings['8501/']}" for the running "container" called "consul" should exist"
  And a HTTP call to "http://consul.container.shipyard.run:8500/v1/status/leader" should result in status 200