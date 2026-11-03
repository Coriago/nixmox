
# E2E Test for nixmox
- use cli interface not internal hookups
- use nushell scripts for e2e testing
- tests must be kept clean, well organized and easy to read. 
- Use helper functions, keep important cli calls visible in tests 
- tests should be split into multiple files if they start getting too long.
- vms will have writable virtual disks
- vms will use current host arch: x86
- gitops should actually pull from github, not mock repo

test/
  scripts/ - scripts for running the tests
  repo/ - gitops repo dir for testing
    .nixmox/recipes/ - recipes used for the tests. includes template files as well.
    flake.nix - inputs nixmox
    flake.lock
    modules/
      base  - a basic configuration for a nix vm
      nixmox - enables and configures nixmox using nixmox module
      testpkg - adds a test pkg 
    hosts/ - all hosts configs will be put here
      vm1/ - initial memeber, other vms will use recipes so we don't have to commit into the repo
        configuration.nix - configuration for vm1 using base and nixmox
        hardware.nix - hardware generation for vm1
        disk.nix - disk layout for vm1

## Test Steps
### single host phase
1. start vm1 with nixmox
2. validate unjoined state
3. create a cluster
- should start with a bootstrap server, and any other cluster init items checked
- validate full-stable status and ensure credential store and cache is ready
- validate host telemetry is streamed
4. add this repo and test/repo dir to gitops sources
5. assign vm1 to the vm1 configuration and test a deployment to vm1
6. add recipe that would modify the vm config and make it install the testpkg module
7. use the recipe to make a configuration for vm1 and deploy it 
- validate the package now exists on vm1 changes are there

### multi-host phase
8. launch another vm with nixmox installer, this will eventually become vm2
9. Use enrollment and discovery to add vm2 using vm1 cluster
- validate pending status and join to cluster
10. use another recipe that will be used to create vm2, with hardware/disk collect
11. use a recipe to create a configuration for vm2 
- recipe should collect hardware info
- recipe should collect disk info
- configuration would generate hosts/vm2
12. do fresh deploy of vm2
- vm2 should install and do finalization step
- disko paritioning is done on the vms disk
- validate is full full member
- validate persistence of information
13. promote vm2 to full-stable.
14. start vm3 but not in an installer config, as an unjoined same as vm1
15. have vm3 join by request into the cluster
16. accept the request via either cluster vm and check that join occurs.
17. force promote vm3 to full then full-stable
- wait until the cluster is healthy and stable
- validate the 3 node jetstream cluster and quorum
- validate bootstrap server has been shutoff
18. reuse recipe used earlier for vm2 to make a vm3 config at hosts/vm3
19. launch vm3 into installer using kexec prepare
- ensure vm3 is demoted into pending
- ensure bootstrap came back online
20. assign vm3 configuration and do fresh installation
- verify full-stable membership is re-established
