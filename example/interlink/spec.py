import datetime
from typing import Dict, List, Optional

from pydantic import BaseModel, Field

class Metadata(BaseModel):
    name: Optional[str] = None
    namespace: Optional[str] = None
    uid: Optional[str] = None
    annotations: Optional[Dict[str, str]] = Field({})
    labels: Optional[Dict[str, str]] = Field({})
    generateName: Optional[str] = None


class VolumeMount(BaseModel):
    name: str
    mountPath: str
    subPath: Optional[str] = None
    readOnly: Optional[bool] = False
    mountPropagation: Optional[str] = None


class ConfigMapKeySelector(BaseModel):
    key: str
    name: Optional[str] = None
    optional: Optional[bool] = None


class SecretKeySelector(BaseModel):
    key: str
    name: Optional[str] = None
    optional: Optional[bool] = None


class EnvVarSource(BaseModel):
    configMapKeyRef: Optional[ConfigMapKeySelector] = None
    secretKeyRef: Optional[SecretKeySelector] = None


class EnvVar(BaseModel):
    name: str
    value: Optional[str] = None
    valueFrom: Optional[EnvVarSource] = None


class SecurityContext(BaseModel):
    allowPrivilegeEscalation: Optional[bool] = None
    privileged: Optional[bool] = None
    procMount: Optional[str] = None
    readOnlyFileSystem: Optional[bool] = None
    runAsGroup: Optional[int] = None
    runAsNonRoot: Optional[bool] = None
    runAsUser: Optional[int] = None


class Container(BaseModel):
    name: str
    image: str
    tag: str = "latest"
    command: List[str]
    args: Optional[List[str]] = Field([])
    resources: Optional[dict] = Field({})
    volumeMounts: Optional[List[VolumeMount]] = Field([])
    env: Optional[List[EnvVar]] = None
    securityContext: Optional[SecurityContext] = None


class KeyToPath(BaseModel):
    key: Optional[str]
    path: str
    mode: Optional[int] = None


class SecretVolumeSource(BaseModel):
    secretName: str
    items: Optional[List[KeyToPath]] = Field([])
    optional: Optional[bool] = None
    defaultMode: Optional[int] = None


class ConfigMapVolumeSource(BaseModel):
    name: str
    items: Optional[List[KeyToPath]] = Field([])
    optional: Optional[bool] = None
    defaultMode: Optional[int] = None


# class VolumeSource(BaseModel):
#     emptyDir: Optional[dict] = None
#     secret: Optional[SecretSource] = None
#     configMap: Optional[ConfigMapVolumeSource] = None


class PodVolume(BaseModel):
    name: str
    #    volumeSource: Optional[VolumeSource] = None
    emptyDir: Optional[dict] = None
    secret: Optional[SecretVolumeSource] = None
    configMap: Optional[ConfigMapVolumeSource] = None


class PodSpec(BaseModel):
    containers: List[Container]
    initContainers: Optional[List[Container]] = None
    volumes: Optional[List[PodVolume]] = None
    preemptionPolicy: Optional[str] = None
    priorityClassName: Optional[str] = None
    priority: Optional[int] = None
    restartPolicy: Optional[str] = None
    terminationGracePeriodSeconds: Optional[int] = None


class PodRequest(BaseModel):
    metadata: Metadata
    spec: PodSpec


class ConfigMap(BaseModel):
    metadata: Metadata
    data: Optional[dict]
    binaryData: Optional[dict] = None
    type: Optional[str] = None
    immutable: Optional[bool] = None


class Secret(BaseModel):
    metadata: Metadata
    data: Optional[dict] = None
    stringData: Optional[dict] = None
    type: Optional[str] = None
    immutable: Optional[bool] = None


class Volume(BaseModel):
    name: str
    configMaps: Optional[List[ConfigMap]] = None
    secrets: Optional[List[Secret]] = None
    emptyDirs: Optional[List[str]] = None


class Pod(BaseModel):
    pod: PodRequest
    container: List[Volume]


class StateTerminated(BaseModel):
    exitCode: int
    reason: Optional[str] = None


class StateRunning(BaseModel):
    startedAt: Optional[str] = None


class StateWaiting(BaseModel):
    message: Optional[str] = None
    reason: Optional[str] = None


class ContainerStates(BaseModel):
    terminated: Optional[StateTerminated] = None
    running: Optional[StateRunning] = None
    waiting: Optional[StateWaiting] = None


class ContainerStatus(BaseModel):
    name: str
    state: ContainerStates


class PodStatus(BaseModel):
    name: str
    UID: str
    namespace: str
    containers: List[ContainerStatus]


class LogOpts(BaseModel):
    Tail: Optional[int] = None
    LimitBytes: Optional[int] = None
    Timestamps: Optional[bool] = None
    Previous: Optional[bool] = None
    SinceSeconds: Optional[int] = None
    SinceTime: Optional[datetime.datetime] = None


class LogRequest(BaseModel):
    Namespace: str
    PodUID: str
    PodName: str
    ContainerName: str
    Opts: LogOpts
