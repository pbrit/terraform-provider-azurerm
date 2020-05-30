package containers

import (
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/containerservice/mgmt/2020-02-01/containerservice"
	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/helper/validation"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/helpers/azure"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/helpers/tf"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/helpers/validate"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/internal/clients"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/internal/services/containers/parse"
	containerValidate "github.com/terraform-providers/terraform-provider-azurerm/azurerm/internal/services/containers/validate"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/internal/tags"
	azSchema "github.com/terraform-providers/terraform-provider-azurerm/azurerm/internal/tf/schema"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/internal/timeouts"
	"github.com/terraform-providers/terraform-provider-azurerm/azurerm/utils"
)

func resourceArmKubernetesClusterNodePool() *schema.Resource {
	return &schema.Resource{
		Create: resourceArmKubernetesClusterNodePoolCreate,
		Read:   resourceArmKubernetesClusterNodePoolRead,
		Update: resourceArmKubernetesClusterNodePoolUpdate,
		Delete: resourceArmKubernetesClusterNodePoolDelete,

		Importer: azSchema.ValidateResourceIDPriorToImport(func(id string) error {
			_, err := parse.KubernetesNodePoolID(id)
			return err
		}),

		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(60 * time.Minute),
			Read:   schema.DefaultTimeout(5 * time.Minute),
			Update: schema.DefaultTimeout(60 * time.Minute),
			Delete: schema.DefaultTimeout(60 * time.Minute),
		},

		CustomizeDiff: customizeDiff,

		Schema: map[string]*schema.Schema{
			"name": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validate.KubernetesAgentPoolName,
			},

			"kubernetes_cluster_id": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: containerValidate.KubernetesClusterID,
			},

			"node_count": {
				Type:         schema.TypeInt,
				Optional:     true,
				Computed:     true,
				ValidateFunc: validation.IntBetween(1, 100),
			},

			"tags": tags.Schema(),

			"vm_size": {
				Type:         schema.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: validation.StringIsNotEmpty,
			},

			// Optional
			"availability_zones": {
				Type:     schema.TypeList,
				Optional: true,
				Elem: &schema.Schema{
					Type: schema.TypeString,
				},
			},

			"enable_auto_scaling": {
				Type:     schema.TypeBool,
				Optional: true,
			},

			"enable_node_public_ip": {
				Type:     schema.TypeBool,
				Optional: true,
			},

			"max_count": {
				Type:     schema.TypeInt,
				Optional: true,
				// NOTE: rather than setting `0` users should instead pass `null` here
				ValidateFunc: validation.IntBetween(1, 100),
			},

			"max_pods": {
				Type:     schema.TypeInt,
				Optional: true,
				Computed: true,
				ForceNew: true,
			},

			"min_count": {
				Type:     schema.TypeInt,
				Optional: true,
				// NOTE: rather than setting `0` users should instead pass `null` here
				ValidateFunc: validation.IntBetween(1, 100),
			},

			"node_labels": {
				Type:     schema.TypeMap,
				Optional: true,
				Computed: true,
				ForceNew: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
			},

			"node_taints": {
				Type:     schema.TypeList,
				Optional: true,
				Computed: true,
				ForceNew: true,
				Elem:     &schema.Schema{Type: schema.TypeString},
			},

			"os_disk_size_gb": {
				Type:         schema.TypeInt,
				Optional:     true,
				ForceNew:     true,
				Computed:     true,
				ValidateFunc: validation.IntAtLeast(1),
			},

			"os_type": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
				Default:  string(containerservice.Linux),
				ValidateFunc: validation.StringInSlice([]string{
					string(containerservice.Linux),
					string(containerservice.Windows),
				}, false),
			},

			"vnet_subnet_id": {
				Type:         schema.TypeString,
				Optional:     true,
				ForceNew:     true,
				ValidateFunc: azure.ValidateResourceID,
			},

			"priority": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
				Default:  string(containerservice.Regular),
				ValidateFunc: validation.StringInSlice([]string{
					string(containerservice.Regular),
					string(containerservice.Spot),
				}, false),
			},

			"eviction_policy": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
				ValidateFunc: validation.StringInSlice([]string{
					string(containerservice.Delete),
					string(containerservice.Deallocate),
				}, false),
			},

			"max_bid_price": {
				Type:         schema.TypeFloat,
				Optional:     true,
				ForceNew:     true,
				ValidateFunc: validate.MaxBidPrice,
			},
		},
	}
}

func customizeDiff(d *schema.ResourceDiff, v interface{}) error {
	priority, _ := d.GetOk("priority")
	_, hasEvictionPolicy := d.GetOk("eviction_policy")
	_, hasMaxBidPrice := d.GetOk("max_bid_price")

	errMsg := ""

	if priority.(string) == string(containerservice.Regular) {
		if hasMaxBidPrice {
			errMsg = "`priority` must be set to `Spot` if `max_bid_price` is specified"
		}

		if hasEvictionPolicy {
			if errMsg != "" {
				errMsg = fmt.Sprintf("%s, `priority` must be set to `Spot` if `eviction_policy` is specified", errMsg)
			} else {
				errMsg = "`priority` must be set to `Spot` if `eviction_policy` is specified"
			}
		}
	}

	if errMsg != "" {
		return errors.New(errMsg)
	}

	return nil
}

func resourceArmKubernetesClusterNodePoolCreate(d *schema.ResourceData, meta interface{}) error {
	clustersClient := meta.(*clients.Client).Containers.KubernetesClustersClient
	poolsClient := meta.(*clients.Client).Containers.AgentPoolsClient
	ctx, cancel := timeouts.ForCreate(meta.(*clients.Client).StopContext, d)
	defer cancel()

	kubernetesClusterId, err := parse.KubernetesClusterID(d.Get("kubernetes_cluster_id").(string))
	if err != nil {
		return err
	}

	resourceGroup := kubernetesClusterId.ResourceGroup
	clusterName := kubernetesClusterId.Name
	name := d.Get("name").(string)

	log.Printf("[DEBUG] Retrieving Kubernetes Cluster %q (Resource Group %q)..", clusterName, resourceGroup)
	cluster, err := clustersClient.Get(ctx, resourceGroup, clusterName)
	if err != nil {
		if utils.ResponseWasNotFound(cluster.Response) {
			return fmt.Errorf("Kubernetes Cluster %q was not found in Resource Group %q!", clusterName, resourceGroup)
		}

		return fmt.Errorf("retrieving existing Kubernetes Cluster %q (Resource Group %q): %+v", clusterName, resourceGroup, err)
	}

	// try to provide a more helpful error here
	defaultPoolIsVMSS := false
	if props := cluster.ManagedClusterProperties; props != nil {
		if pools := props.AgentPoolProfiles; pools != nil {
			for _, p := range *pools {
				if p.Type == containerservice.VirtualMachineScaleSets {
					defaultPoolIsVMSS = true
					break
				}
			}
		}
	}
	if !defaultPoolIsVMSS {
		return fmt.Errorf("The Default Node Pool for Kubernetes Cluster %q (Resource Group %q) must be a VirtualMachineScaleSet to attach multiple node pools!", clusterName, resourceGroup)
	}

	if d.IsNewResource() {
		existing, err := poolsClient.Get(ctx, resourceGroup, clusterName, name)
		if err != nil {
			if !utils.ResponseWasNotFound(existing.Response) {
				return fmt.Errorf("checking for presence of existing Agent Pool %q (Kubernetes Cluster %q / Resource Group %q): %s", name, clusterName, resourceGroup, err)
			}
		}

		if existing.ID != nil && *existing.ID != "" {
			return tf.ImportAsExistsError("azurerm_kubernetes_cluster_node_pool", *existing.ID)
		}
	}

	count := d.Get("node_count").(int)
	enableAutoScaling := d.Get("enable_auto_scaling").(bool)
	osType := d.Get("os_type").(string)
	t := d.Get("tags").(map[string]interface{})
	vmSize := d.Get("vm_size").(string)
	scaleSetPriority := d.Get("priority").(string)

	profile := containerservice.ManagedClusterAgentPoolProfileProperties{
		OsType:             containerservice.OSType(osType),
		EnableAutoScaling:  utils.Bool(enableAutoScaling),
		EnableNodePublicIP: utils.Bool(d.Get("enable_node_public_ip").(bool)),
		Tags:               tags.Expand(t),
		Type:               containerservice.VirtualMachineScaleSets,
		VMSize:             containerservice.VMSizeTypes(vmSize),
		ScaleSetPriority:   containerservice.ScaleSetPriority(scaleSetPriority),

		// this must always be sent during creation, but is optional for auto-scaled clusters during update
		Count: utils.Int32(int32(count)),
	}

	availabilityZonesRaw := d.Get("availability_zones").([]interface{})
	if availabilityZones := utils.ExpandStringSlice(availabilityZonesRaw); len(*availabilityZones) > 0 {
		profile.AvailabilityZones = availabilityZones
	}

	if maxPods := int32(d.Get("max_pods").(int)); maxPods > 0 {
		profile.MaxPods = utils.Int32(maxPods)
	}

	nodeLabelsRaw := d.Get("node_labels").(map[string]interface{})
	if nodeLabels := utils.ExpandMapStringPtrString(nodeLabelsRaw); len(nodeLabels) > 0 {
		profile.NodeLabels = nodeLabels
	}

	nodeTaintsRaw := d.Get("node_taints").([]interface{})
	if nodeTaints := utils.ExpandStringSlice(nodeTaintsRaw); len(*nodeTaints) > 0 {
		profile.NodeTaints = nodeTaints
	}

	if osDiskSizeGB := d.Get("os_disk_size_gb").(int); osDiskSizeGB > 0 {
		profile.OsDiskSizeGB = utils.Int32(int32(osDiskSizeGB))
	}

	if vnetSubnetID := d.Get("vnet_subnet_id").(string); vnetSubnetID != "" {
		profile.VnetSubnetID = utils.String(vnetSubnetID)
	}

	if scaleSetEvictionPolicy := d.Get("eviction_policy").(string); scaleSetEvictionPolicy != "" {
		profile.ScaleSetEvictionPolicy = containerservice.ScaleSetEvictionPolicy(scaleSetEvictionPolicy)
	}

	if spotPrice := d.Get("max_bid_price").(float64); spotPrice != 0 {
		profile.SpotMaxPrice = utils.Float(spotPrice)
	}

	maxCount := d.Get("max_count").(int)
	minCount := d.Get("min_count").(int)

	if enableAutoScaling {
		// handle count being optional
		if count == 0 {
			profile.Count = utils.Int32(int32(minCount))
		}

		if maxCount > 0 {
			profile.MaxCount = utils.Int32(int32(maxCount))
		} else {
			return fmt.Errorf("`max_count` must be configured when `enable_auto_scaling` is set to `true`")
		}

		if minCount > 0 {
			profile.MinCount = utils.Int32(int32(minCount))
		} else {
			return fmt.Errorf("`min_count` must be configured when `enable_auto_scaling` is set to `true`")
		}

		if minCount > maxCount {
			return fmt.Errorf("`max_count` must be >= `min_count`")
		}
	} else if minCount > 0 || maxCount > 0 {
		return fmt.Errorf("`max_count` and `min_count` must be set to `null` when enable_auto_scaling is set to `false`")
	}

	parameters := containerservice.AgentPool{
		Name:                                     &name,
		ManagedClusterAgentPoolProfileProperties: &profile,
	}

	future, err := poolsClient.CreateOrUpdate(ctx, resourceGroup, clusterName, name, parameters)
	if err != nil {
		return fmt.Errorf("creating/updating Managed Kubernetes Cluster Node Pool %q (Resource Group %q): %+v", name, resourceGroup, err)
	}

	if err = future.WaitForCompletionRef(ctx, poolsClient.Client); err != nil {
		return fmt.Errorf("waiting for completion of Managed Kubernetes Cluster Node Pool %q (Resource Group %q): %+v", name, resourceGroup, err)
	}

	read, err := poolsClient.Get(ctx, resourceGroup, clusterName, name)
	if err != nil {
		return fmt.Errorf("retrieving Managed Kubernetes Cluster Node Pool %q (Resource Group %q): %+v", name, resourceGroup, err)
	}

	if read.ID == nil {
		return fmt.Errorf("Cannot read ID for Managed Kubernetes Cluster Node Pool %q (Resource Group %q)", name, resourceGroup)
	}

	d.SetId(*read.ID)

	return resourceArmKubernetesClusterNodePoolRead(d, meta)
}

func resourceArmKubernetesClusterNodePoolUpdate(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*clients.Client).Containers.AgentPoolsClient
	ctx, cancel := timeouts.ForUpdate(meta.(*clients.Client).StopContext, d)
	defer cancel()

	id, err := parse.KubernetesNodePoolID(d.Id())
	if err != nil {
		return err
	}

	d.Partial(true)

	log.Printf("[DEBUG] Retrieving existing Node Pool %q (Kubernetes Cluster %q / Resource Group %q)..", id.Name, id.ClusterName, id.ResourceGroup)
	existing, err := client.Get(ctx, id.ResourceGroup, id.ClusterName, id.Name)
	if err != nil {
		if utils.ResponseWasNotFound(existing.Response) {
			return fmt.Errorf("[DEBUG] Node Pool %q was not found in Managed Kubernetes Cluster %q / Resource Group %q!", id.Name, id.ClusterName, id.ResourceGroup)
		}

		return fmt.Errorf("retrieving Node Pool %q (Managed Kubernetes Cluster %q / Resource Group %q): %+v", id.Name, id.ClusterName, id.ResourceGroup, err)
	}
	if existing.ManagedClusterAgentPoolProfileProperties == nil {
		return fmt.Errorf("retrieving Node Pool %q (Managed Kubernetes Cluster %q / Resource Group %q): `properties` was nil", id.Name, id.ClusterName, id.ResourceGroup)
	}

	props := existing.ManagedClusterAgentPoolProfileProperties

	// store the existing value should the user have opted to ignore it
	enableAutoScaling := false
	if props.EnableAutoScaling != nil {
		enableAutoScaling = *props.EnableAutoScaling
	}

	log.Printf("[DEBUG] Determining delta for existing Node Pool %q (Kubernetes Cluster %q / Resource Group %q)..", id.Name, id.ClusterName, id.ResourceGroup)

	// delta patching
	if d.HasChange("availability_zones") {
		availabilityZonesRaw := d.Get("availability_zones").([]interface{})
		availabilityZones := utils.ExpandStringSlice(availabilityZonesRaw)
		props.AvailabilityZones = availabilityZones
	}

	if d.HasChange("enable_auto_scaling") {
		enableAutoScaling = d.Get("enable_auto_scaling").(bool)
		props.EnableAutoScaling = utils.Bool(enableAutoScaling)
	}

	if d.HasChange("enable_node_public_ip") {
		props.EnableNodePublicIP = utils.Bool(d.Get("enable_node_public_ip").(bool))
	}

	if d.HasChange("max_count") {
		props.MaxCount = utils.Int32(int32(d.Get("max_count").(int)))
	}

	if d.HasChange("min_count") {
		props.MinCount = utils.Int32(int32(d.Get("min_count").(int)))
	}

	if d.HasChange("node_count") {
		props.Count = utils.Int32(int32(d.Get("node_count").(int)))
	}

	if d.HasChange("tags") {
		t := d.Get("tags").(map[string]interface{})
		props.Tags = tags.Expand(t)
	}

	// validate the auto-scale fields are both set/unset to prevent a continual diff
	maxCount := 0
	if props.MaxCount != nil {
		maxCount = int(*props.MaxCount)
	}
	minCount := 0
	if props.MinCount != nil {
		minCount = int(*props.MinCount)
	}
	if enableAutoScaling {
		if maxCount == 0 {
			return fmt.Errorf("`max_count` must be configured when `enable_auto_scaling` is set to `true`")
		}
		if minCount == 0 {
			return fmt.Errorf("`min_count` must be configured when `enable_auto_scaling` is set to `true`")
		}

		if minCount > maxCount {
			return fmt.Errorf("`max_count` must be >= `min_count`")
		}
	} else {
		if minCount > 0 || maxCount > 0 {
			return fmt.Errorf("`max_count` and `min_count` must be set to `nil` when enable_auto_scaling is set to `false`")
		}

		// @tombuildsstuff: as of API version 2019-11-01 we need to explicitly nil these out
		props.MaxCount = nil
		props.MinCount = nil
	}

	log.Printf("[DEBUG] Updating existing Node Pool %q (Kubernetes Cluster %q / Resource Group %q)..", id.Name, id.ClusterName, id.ResourceGroup)
	existing.ManagedClusterAgentPoolProfileProperties = props
	future, err := client.CreateOrUpdate(ctx, id.ResourceGroup, id.ClusterName, id.Name, existing)
	if err != nil {
		return fmt.Errorf("updating Node Pool %q (Kubernetes Cluster %q / Resource Group %q): %+v", id.Name, id.ClusterName, id.ResourceGroup, err)
	}

	if err = future.WaitForCompletionRef(ctx, client.Client); err != nil {
		return fmt.Errorf("waiting for update of Node Pool %q (Kubernetes Cluster %q / Resource Group %q): %+v", id.Name, id.ClusterName, id.ResourceGroup, err)
	}

	d.Partial(false)

	return resourceArmKubernetesClusterNodePoolRead(d, meta)
}

func resourceArmKubernetesClusterNodePoolRead(d *schema.ResourceData, meta interface{}) error {
	clustersClient := meta.(*clients.Client).Containers.KubernetesClustersClient
	poolsClient := meta.(*clients.Client).Containers.AgentPoolsClient
	ctx, cancel := timeouts.ForRead(meta.(*clients.Client).StopContext, d)
	defer cancel()

	id, err := parse.KubernetesNodePoolID(d.Id())
	if err != nil {
		return err
	}

	// if the parent cluster doesn't exist then the node pool won't
	cluster, err := clustersClient.Get(ctx, id.ResourceGroup, id.ClusterName)
	if err != nil {
		if utils.ResponseWasNotFound(cluster.Response) {
			log.Printf("[DEBUG] Managed Kubernetes Cluster %q was not found in Resource Group %q - removing from state!", id.ClusterName, id.ResourceGroup)
			d.SetId("")
			return nil
		}

		return fmt.Errorf("retrieving Managed Kubernetes Cluster %q (Resource Group %q): %+v", id.ClusterName, id.ResourceGroup, err)
	}

	resp, err := poolsClient.Get(ctx, id.ResourceGroup, id.ClusterName, id.Name)
	if err != nil {
		if utils.ResponseWasNotFound(resp.Response) {
			log.Printf("[DEBUG] Node Pool %q was not found in Managed Kubernetes Cluster %q / Resource Group %q - removing from state!", id.Name, id.ClusterName, id.ResourceGroup)
			d.SetId("")
			return nil
		}

		return fmt.Errorf("retrieving Node Pool %q (Managed Kubernetes Cluster %q / Resource Group %q): %+v", id.Name, id.ClusterName, id.ResourceGroup, err)
	}

	d.Set("name", id.Name)
	d.Set("kubernetes_cluster_id", cluster.ID)

	if props := resp.ManagedClusterAgentPoolProfileProperties; props != nil {
		if err := d.Set("availability_zones", utils.FlattenStringSlice(props.AvailabilityZones)); err != nil {
			return fmt.Errorf("setting `availability_zones`: %+v", err)
		}

		d.Set("enable_auto_scaling", props.EnableAutoScaling)
		d.Set("enable_node_public_ip", props.EnableNodePublicIP)

		maxCount := 0
		if props.MaxCount != nil {
			maxCount = int(*props.MaxCount)
		}
		d.Set("max_count", maxCount)

		maxPods := 0
		if props.MaxPods != nil {
			maxPods = int(*props.MaxPods)
		}
		d.Set("max_pods", maxPods)

		minCount := 0
		if props.MinCount != nil {
			minCount = int(*props.MinCount)
		}
		d.Set("min_count", minCount)

		count := 0
		if props.Count != nil {
			count = int(*props.Count)
		}
		d.Set("node_count", count)

		if err := d.Set("node_labels", props.NodeLabels); err != nil {
			return fmt.Errorf("setting `node_labels`: %+v", err)
		}

		if err := d.Set("node_taints", utils.FlattenStringSlice(props.NodeTaints)); err != nil {
			return fmt.Errorf("setting `node_taints`: %+v", err)
		}

		osDiskSizeGB := 0
		if props.OsDiskSizeGB != nil {
			osDiskSizeGB = int(*props.OsDiskSizeGB)
		}
		d.Set("os_disk_size_gb", osDiskSizeGB)
		d.Set("os_type", string(props.OsType))
		d.Set("vnet_subnet_id", props.VnetSubnetID)
		d.Set("vm_size", string(props.VMSize))

		d.Set("priority", string(props.ScaleSetPriority))
		d.Set("eviction_policy", string(props.ScaleSetEvictionPolicy))
		d.Set("max_bid_price", props.SpotMaxPrice)
	}

	return tags.FlattenAndSet(d, resp.Tags)
}

func resourceArmKubernetesClusterNodePoolDelete(d *schema.ResourceData, meta interface{}) error {
	client := meta.(*clients.Client).Containers.AgentPoolsClient
	ctx, cancel := timeouts.ForDelete(meta.(*clients.Client).StopContext, d)
	defer cancel()

	id, err := parse.KubernetesNodePoolID(d.Id())
	if err != nil {
		return err
	}

	future, err := client.Delete(ctx, id.ResourceGroup, id.ClusterName, id.Name)
	if err != nil {
		return fmt.Errorf("deleting Node Pool %q (Managed Kubernetes Cluster %q / Resource Group %q): %+v", id.Name, id.ClusterName, id.ResourceGroup, err)
	}

	if err := future.WaitForCompletionRef(ctx, client.Client); err != nil {
		return fmt.Errorf("waiting for the deletion of Node Pool %q (Managed Kubernetes Cluster %q / Resource Group %q): %+v", id.Name, id.ClusterName, id.ResourceGroup, err)
	}

	return nil
}
