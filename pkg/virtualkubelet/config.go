package virtualkubelet

// VirtualKubeletConfig holds the whole configuration
type VirtualKubeletConfig struct {
	Interlinkurl      string `yaml:"InterlinkURL"`
	Interlinkport     string `yaml:"InterlinkPort"`
	VKConfigPath      string `yaml:"VKConfigPath"`
	VKTokenFile       string `yaml:"VKTokenFile"`
	ServiceAccount    string `yaml:"ServiceAccount"`
	Namespace         string `yaml:"Namespace"`
	PodIP             string `yaml:"PodIP"`
	VerboseLogging    bool   `yaml:"VerboseLogging"`
	ErrorsOnlyLogging bool   `yaml:"ErrorsOnlyLogging"`
	CPU               string `yaml:"cpu,omitempty"`
	Memory            string `yaml:"memory,omitempty"`
	Pods              string `yaml:"pods,omitempty"`
	GPU               string `yaml:"nvidia.com/gpu,omitempty"`
}
