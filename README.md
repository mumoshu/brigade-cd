# Brigade CD: GitOps-based Continuous Delivery Gateway for Brigade

`brigade-cd` is a [Brigade](https://github.com/brigadecore/brigade) gateway that augments [Flux](https://github.com/fluxcd/flux) so that it delegates complex deployments to Brigade builds.

## Installation

The installation for this gateway is multi-part, and not particularly easy at
the moment.

Prerequisites:

- A Kubernetes cluster running Brigade
- kubectl and Helm

You will also need to pick out a domain name (referenced as *YOUR_DOMAIN* below)
to send GitHub requests to. Example: `gh-gateway.example.com`. If you don't want
to do this, see the notes in Step 3.

### 1. Create a GitHub App

A GitHub app is a special kind of trusted entity that is associated with either
your account or an orgs account.

Create one following the instruction for [Brigade GitHub App](https://github.com/brigadecore/brigade-github-app#1-create-a-github-app).

### 2. Install the Helm chart into your cluster

The [Brigade CD Helm Chart](brigade-cd-chart) is hosted within this repository.

You must install this gateway into the same namespace in your cluster where
Brigade is already running.

```
$ helm repo add brigadecd https://example.com/charts
$ helm inspect values brigadecd/brigade-cd > values.yaml
$ # Edit values.yaml
$ helm install -n gh-app brigadecd/brigade-cd
```

> The private key you created in Step 1 should be put in your `values.yaml` file:

```yaml
# Other stuff...
github:
  key: |
    YOUR KEY DATA GOES HERE!
    AND EVERY LINE NEEDS TO BE INDENTED
```

On RBAC-enabled clusters, pass `--set rbac.enabled=true` to the `helm install`
command.

### 3. Install the App

Go to the _Install App_ tab and enable this app for your account.

Accept the permissions it asks for. You can choose between _All repos_ and
_Only select repositories_, and click _Install_

> It is easy to change from All Repos to Only Selected, and vice versa, so we
> recommend starting with one repo, and adding the rest later.

### 6. Add Brigade projects for each GitHub project

For each GitHub project that you enabled the app for, you will now need to
create a Project.

Remember that projects contain secret data, and should be handled with care.

```
$ helm inspect values brigade/brigade-project > values.yaml
$ # Edit values.yaml
```

You will want to make sure to set:

- `project`, `repository`, and `cloneURL`  to point to your repo
- `sharedSecret` to use the shared secret you created when creating the app

To run brigade-cd deployments within GitHub check runs, you will need to provide the ID for your GitHub Brigade App instance.
(Here also set at the chart-level via `values.yaml`):

```
github:
...
  appID: APP_ID
```

This value is provided after the GitHub App is created on GitHub (see 1. Create a GitHub App). To find this value after creation, visit `https://github.com/settings/apps/your-app-name`.

> Using the application ID and the private key configured when deploying the Helm chart, this gateway creates a new GitHub token for each request, meaning that we don't have to create a per-repository token.

When these parameters are set, incoming pull requests will also trigger `check_suite:created` events.

## Handling Events in `brigade.js`

This gateway behaves differently than the gateway that ships with Brigade.
Because this is a Kubernetes controller that watches and reconciles Kubernetes custom resources, all it can do is pass updated resource to your `brigade.js`.

That is, an updated resource is sent in the payload, which looks like this:

```json
{
  "token": "some.really.long.string",
  "body": {
    "apiVersion": "helmfile.helm.sh/v1alpha1",
    "kind": "ReleaseSet",
    "metadata": {
      "name": "myapp",
      "annotations": {
        "approved": "true"
      }
    },
    "spec": {
    }
  }
}
```

The above shows just the very top level of the object. The object you will
really receive will be much more detailed, according to your custom resource definition.

### Events Emitted by this Gateway

All the kinds of changes made in your custom resource received by this gateway from Kubernetes are, in turn, emitted into
Brigade. In some cases, events received from Github contain an `action` field.
For all such events, _two_ events will be emitted into Brigade. One will be a
coarse-grained event, unaqualified by `action`. The second will be more
finely-grained and qualified by `action`. The latter permits Brigade users to to
more easily subscribe to a relevant subset of events that are of interest to
them.

The events emitted by this gateway into Brigade are:

- `<kind>>`: An update event with any `action`. A second event qualified by `action` will _also_ be emitted.
- `<kind>:apply`: The custom resource has been updated and committed
- `<kind>:plan`: The custom resource has been updated, but not commited(missing `approved: true` annotation)
- `<kind>:destroy`: The custom resource has been removed

### Reconciling custom resource on change

Currently this gateway forwards all events on to the Brigade.js script, and does
nothing else. The `brigade.js` must run necessary commands to actually create/update/delete the dependent resources.

Here's an example that starts a new run, does a test, and then marks that
run complete. On error, it marks the run failed.

```javascript
const {events, Job, Group} = require("brigadier");
const checkRunImage = "brigadecore/brigade-github-check-run:latest"

events.on("releaseset:apply", handleReleaseSet("apply"))
events.on("releaseset:plan", handleReleaseSet("plan"))
events.on("releaseset:destroy", handleReleaseSet("destroy"))

function handleReleaseSet(action) {
    return async (e, p) => {
        let payload = JSON.parse(e.payload);
        let body = payload.body

        function newCheckRunStart() {
            return {
                'name': `brigade-cd-${payload.type}-${action}`,
                'head_sha': payload.commit,
                'status': "in_progress",
                'started_at': new Date().toISOString(),
            }
        }

        function newCheckRunEnd(conclusion, title, summary, text) {
            let run = newCheckRunStart()
            run['completed_at'] = new Date().toISOString()
            run['output'] = {
                'title': title,
                'summary': summary,
                'text': text
            }
            run['conclusion'] = conclusion
            return run
        }

        async function gatherLogs(build) {
            let logs = "N/A"
            try {
                logs = await build.logs()
            } catch (err2) {
                console.log("failed while gathering logs", err2)
            }
            return logs
        }

        let run = newCheckRunStart()

        let build = null
        let opts = {streamLogs: true}
        switch (action) {
            case "plan":
                // We have no way to run use the brigade's built-in check-run container to create/update check runs for payloads sent from brigade-cd
                // await checkWithHelmfile("diff", pr, e.payload, p)
                build = command("diff", opts)
                break
            case "apply":
                build = command("apply", opts)
                break
            case "destroy":
                build = command("destroy", opts)
                break
            default:
                break
        }
        try {
            let result = await build.run()
            // let logs = await gatherLogs(build)
            let text = `Logs:
${result.toString()}`
            let r = newCheckRunEnd("success", "Result", `${action} succeeded`, text)
            await gh.createCheckRun(payload.owner, payload.repo, r, token)
        } catch (err) {
            let logs = await gatherLogs(build)
            let text = `${err}

Logs:
${logs}`
            let r = newCheckRunEnd("failure", "Result", `${action} failed\\n\\n${lastLines(text, 10)}`, text)
        }
    }
}

// command creates a Brigade job for running `cmd`.
function command(cmd, opts) {
    let job = new Job(cmd, image)
    job.tasks = [
        "mkdir -p " + dest,
        "cp -a /src/* " + dest,
        "cd " + dest,
        `helmfile ${cmd}`,
    ]
    if (typeof opts == "object") {
        for (let k of Object.keys(opts)) {
            job[k] = opts[k]
        }
    }
    return job
}
```

## Further Examples

See [`brigade.js` in the demo repository](https://github.com/mumoshu/demo-78a64c769a615eb776/blob/master/brigade.js)
for further `brigade.js` examples exercising
different event handling scenarios, giving the user feedbacks via GitHub PR comments, including Issue/PR comment handling,
and more.

## Building From Source

Prerequisites:

- `make`
- Docker

To build from source:

```console
$ make lint          # to run linters
$ make test          # to run tests
$ make build         # to run multi-stage Docker build of binaries and images
```

## Pushing Images

By default, built images are named using the following scheme:
`<component>:<version>`. If you wish to push customized or experimental images
you have built from source to a particular org on a particular Docker registry,
this can be controlled with environment variables.

The following, for instance, will build images that can be pushed to the
`mumoshu` org on Dockerhub (the registry that is implied when none is
specified).

```console
$ DOCKER_ORG=mumoshu make build
```

To build for the `mumoshu` org on a different registry, such as `quay.io`:

```console
$ DOCKER_REGISTRY=quay.io DOCKER_ORG=mumoshu make build
```

Images built with names that specify registries and orgs for which you have
write access can be pushed using `make push`. Note that the `build` target is
a dependency for the `push` target, so the build _and_ push processes can be
accomplished together like so:

Note also that you _must_ be logged into the registry in question _before_
attempting this.

```console
$ DOCKER_REGISTRY=quay.io DOCKER_ORG=mumoshu make push
```

# Contributing

This Brigade project accepts contributions via GitHub pull requests. This document outlines the process to help get your contribution accepted.

## Signed commits

A DCO sign-off is required for contributions to repos in the brigadecore org.  See the documentation in
[Brigade's Contributing guide](https://github.com/brigadecore/brigade/blob/master/CONTRIBUTING.md#signed-commits)
for how this is done.
