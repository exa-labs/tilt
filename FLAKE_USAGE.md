# Using the Exa Tilt Flake in Monorepo

This flake provides a reproducible build of our Tilt fork with the port-forward reconnection fix (GIGA-1010).

## Integration with Monorepo

To use this custom Tilt build in the monorepo instead of the nixpkgs version:

### 1. Add as a flake input

In `monorepo/flake.nix`, add to the `inputs` section:

```nix
inputs = {
  nixpkgs.url = "github:nixos/nixpkgs?rev=08ec31770452db15496f915f41bbf0a01fa69016";
  depot.url = "path:./flakes/depot";
  depot.inputs.nixpkgs.follows = "nixpkgs";
  
  # Add this:
  tilt.url = "github:exa-labs/tilt";
  tilt.inputs.nixpkgs.follows = "nixpkgs";
};
```

### 2. Pass tilt to shell.nix

Update the `outputs` function to pass the tilt package:

```nix
outputs = {
  self,
  nixpkgs,
  depot,
  tilt,  # Add this
}:
let
  # ... existing code ...
  shellConfig = { system }:
    import ./shell.nix {
      pkgs = import nixpkgs { inherit system; };
      depot = depot.packages.${system}.depot;
      tilt = tilt.packages.${system}.tilt;  # Add this
    };
in
# ... rest of outputs ...
```

### 3. Use in shell.nix

In `monorepo/shell.nix`, accept and use the tilt parameter:

```nix
{
  pkgs,
  depot,
  tilt,  # Add this parameter
}:

pkgs.mkShell {
  buildInputs = [
    # ... existing packages ...
    tilt  # Use our custom tilt instead of pkgs.tilt
    # ... rest of packages ...
  ];
  # ... rest of shell config ...
}
```

## Alternative: Direct Usage

You can also use the flake directly without modifying the monorepo:

```bash
# Run tilt from the flake
nix run github:exa-labs/tilt

# Or add to your shell temporarily
nix shell github:exa-labs/tilt
```

## What's Included

- **Port-forward reconnection fix**: Automatically restarts pod watches when they close, preventing hanging port-forwards (GIGA-1010)
- **Web assets**: Pre-built TypeScript/React UI
- **Shell completions**: bash, fish, and zsh completions
- **Version**: 0.36.0-exa (based on upstream 0.35.2 + our fixes)

## Building Locally

```bash
# Build the package
nix build github:exa-labs/tilt

# Or from a local checkout
cd /path/to/exa-labs/tilt
nix build
```

## Version Updates

When updating to a newer upstream Tilt version:

1. Update the `version` in `flake.nix`
2. Update the `outputHash` in the `yarnOfflineCache` derivation if web dependencies changed
3. Test the build with `nix build`
4. Update this documentation if integration steps change
