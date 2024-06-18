# INTERLINK E2E INTEGRATION TESTS

Here you can find how to test a virtual kubelet implementation against the main pod use cases we mean to support. 

## Requirements

- [Docker engine](https://docs.docker.com/engine/install/)
- [Dagger CLI](https://docs.dagger.io/install/)

## What's in the Dagger module

- E2e integration tests: a reproducible test environment (selfcontained in Dagger runtime). Run the very same tests executed by github actions to validate any PR
- A development setup tool: optionally you can use your k8s cluster of choice to run and install different interlink components via this module.

:warning: by default the docker plugin is the one tested and to be referred to for any change as first thing.

## Usage

The whole test suite is based on the application of k8s manifests inside a folder that must be passed at runtime. In `./ci/manifests` of this repo you can find the one executed by default by the github actions.

That means you can test your code **before** any commit, discovering in advance if anything is breaking.

### Run e2e tests

#### Edit manifests with your images

- `service-account.yaml` is the default set of permission needed by the virtualkubelet. Do not touch unless you know what you are doing.
- `virtual-kubelet-config.yaml` is the configuration mounted into the __virtual kubelet__ component to determine its behaviour.
- `virtual-kubelet.yaml` is the one that you should touch if you are pointing to different interlink endpoints or if you want to change the __virtual kubelet__ image to be tested.
- `interlink-config.yaml` is the configuration mounted into the __interlink API__ component to determine its behaviour.
- `interlink.yaml` is the one that you should touch if you are pointing to different plugin endpoints or if you want to change the __interlink API__ image to be tested.
- `plugin-config.yaml` is the configuration mounted into the __interLink plugin__ component to determine its behaviour.
- `plugin.yaml` is the one that you should touch if you are pointing to different plugin endpoints or if you want to change the __interlink plugin__ image to be tested.


#### Run the tests

First of all, in `ci/manifests/vktest_config.yaml` you will find the pytest configuration file. Please see the [test documentation](https://github.com/interTwin-eu/vk-test-set/tree/main) for understanding how to tweak it. 

The following instructions are thought for building docker images of the virtual-kubelet and interlink api server components at runtime and published on `virtual-kubelet-ref` and `interlink-ref` repositories (in this example it will be dockerHUB repository of the dciangot user).
It basically consists on a chain of Dagger tasks for building core images (`build-images`), creating the kubernetes environment configured with core components (`new-interlink`), installing the plugin of choice indicated in the `manifest` folder (`load-plugin`), and eventually the execution of the tests (`test`)

To run the default tests you can move to `ci` folder and execute the Dagger pipeline with:

```bash
export YOUR_DOCKERHUB_USER=dciangot

dagger  call build-images --source-folder ../ --virtual-kubelet-ref ${YOUR_DOCKERHUB_USER}/vk --interlink-ref ${YOUR_DOCKERHUB_USER}/interlink \
             new-interlink --manifests $PWD/manifests \
             load-plugin \
             test stdout
```

:warning: by default the docker plugin is the one tested and to be referred to for any change as first thing.

In case of success the output should print something like the following:

```text
cachedir: .pytest_cache
rootdir: /opt/vk-test-set
configfile: pyproject.toml
collecting ... collected 12 items / 1 deselected / 11 selected

vktestset/basic_test.py::test_namespace_exists[default] PASSED           [  9%]
vktestset/basic_test.py::test_namespace_exists[kube-system] PASSED       [ 18%]
vktestset/basic_test.py::test_namespace_exists[interlink] PASSED         [ 27%]
vktestset/basic_test.py::test_node_exists[virtual-kubelet] PASSED        [ 36%]
vktestset/basic_test.py::test_manifest[virtual-kubelet-000-hello-world.yaml] PASSED [ 45%]
vktestset/basic_test.py::test_manifest[virtual-kubelet-010-simple-python.yaml] PASSED [ 54%]
vktestset/basic_test.py::test_manifest[virtual-kubelet-020-python-env.yaml] PASSED [ 63%]
vktestset/basic_test.py::test_manifest[virtual-kubelet-030-simple-shared-volume.yaml] PASSED [ 72%]
vktestset/basic_test.py::test_manifest[virtual-kubelet-040-config-volumes.yaml] PASSED [ 81%]
vktestset/basic_test.py::test_manifest[virtual-kubelet-050-limits.yaml] PASSED [ 90%]
vktestset/basic_test.py::test_manifest[virtual-kubelet-060-init-container.yaml] PASSED [100%]

====================== 11 passed, 1 deselected in 41.71s =======================
```

#### Debug with interactive session

In case something went wrong, you have the possibility to spawn a session inside the final step of the pipeline to debug things:

```bash
dagger  call build-images --source-folder ../ --virtual-kubelet-ref dciangot/vk --interlink-ref dciangot/interlink \
             new-interlink --manifests $PWD/manifests \
             load-plugin \
             run terminal
```

with this command (after some minutes) then you should be able to access a bash session doing the following commands:

```bash
bash
source .venv/bin/activate
export KUBECONFIG=/.kube/config

## check connectivity with k8s cluster
kubectl get pod -A

## re-run the tests
pytest -vk 'not rclone'
```


### Deploy on existing K8s cluster

You might want to hijack the test machinery in order to have it instantiating the test environemnt on your own kubernetes cluster (e.g. to debug and develop plugins in a efficient way). We are introducing options for this purpose and it is expected to be extended even more in the future.

If you have a kubernetes cluster **publically accessible**, you can pass your kubeconfig to the Dagger pipeline and use that instead of the internal one that is "one-shot" for the tests only.

```bash
dagger  call build-images --source-folder ../ --virtual-kubelet-ref dciangot/vk --interlink-ref dciangot/interlink \
             new-interlink --manifests $PWD/manifests --kubeconfig $PWD/kubeconfig.yaml \
             load-plugin \
             test stdout
```

If you have a *local* cluster (e.g. via MiniKube), you need to forward the local port of the Kubernetes API server (look inside the kubeconfig file) inside the Dagger runtime with the following:

```bash
dagger  call build-images --source-folder ../ --virtual-kubelet-ref dciangot/vk --interlink-ref dciangot/interlink \
             new-interlink --manifests $PWD/manifests --kubeconfig $PWD/kubeconfig.yaml --local-cluster tcp://localhost:59127 \
             load-plugin \
             test stdout
```

### Develop Virtual Kubelet code

:warning: Coming soon

### Develop Interlink API code

:warning: Coming soon


