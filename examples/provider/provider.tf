terraform {
  required_providers {
    xcelsior = {
      source = "aabiro/xcelsior"
    }
  }
}

# The token comes from XCELSIOR_API_TOKEN when api_token is not set.
provider "xcelsior" {
  # Wait up to 15 minutes for a person to approve a launch that is outside
  # the token's spend envelope. The default, 0s, fails at once.
  approval_timeout = "15m"
}
