package machines

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// The AWS provider: one EC2 instance per node, Ubuntu from Canonical's
// account, as Coder's aws-linux template (examples/templates/aws-linux). The
// instance is found again by its tags, so nothing but the node's id has to
// be remembered to destroy it.

const (
	// Tags on every instance and its volume. The provider tag keeps two
	// control planes that share an AWS account apart.
	awsTagProvider = "hypomnema-provider"
	awsTagNode     = "hypomnema-node"

	// canonicalOwner is Canonical's AWS account, which publishes Ubuntu.
	canonicalOwner = "099720109477"
	// ubuntuImage is Ubuntu 24.04's image name, with the architecture in it.
	ubuntuImage = "ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-%s-server-*"

	awsDiskGiBDefault = 40
)

// AWS option names, set on the provider record.
const (
	// AWSOptionSubnet is the subnet instances start in; the default VPC's when empty.
	AWSOptionSubnet = "subnet_id"
	// AWSOptionSecurityGroups is a comma-separated list of security group ids.
	AWSOptionSecurityGroups = "security_group_ids"
	// AWSOptionKeyName is an EC2 key pair for SSH; none when empty.
	AWSOptionKeyName = "key_name"
	// AWSOptionDiskGiB is the root volume's size.
	AWSOptionDiskGiB = "disk_gib"
)

// awsOptions are the options an AWS provider record may hold.
var awsOptions = []string{AWSOptionSubnet, AWSOptionSecurityGroups, AWSOptionKeyName, AWSOptionDiskGiB}

// ec2API is what the provider calls of EC2.
type ec2API interface {
	DescribeInstanceTypes(context.Context, *ec2.DescribeInstanceTypesInput, ...func(*ec2.Options),
	) (*ec2.DescribeInstanceTypesOutput, error)
	DescribeImages(context.Context, *ec2.DescribeImagesInput, ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error)
	RunInstances(context.Context, *ec2.RunInstancesInput, ...func(*ec2.Options)) (*ec2.RunInstancesOutput, error)
	DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options),
	) (*ec2.DescribeInstancesOutput, error)
	TerminateInstances(context.Context, *ec2.TerminateInstancesInput, ...func(*ec2.Options),
	) (*ec2.TerminateInstancesOutput, error)
}

type awsProvider struct {
	providerID string
	subnet     string
	groups     []string
	keyName    string
	diskGiB    int32
	// client returns EC2 in a region.
	client func(region string) ec2API
}

// ValidateAWSOptions says whether a provider record's options are ones AWS takes.
func ValidateAWSOptions(options map[string]string) error {
	_, err := parseAWSOptions(options)

	return err
}

// NewAWS returns the provider for an AWS provider record. key is the vault
// secret's value: an access key id and its secret access key, joined by a colon.
func NewAWS(rec orchestration.MachineProvider, key string) (Provider, error) {
	id, secret, ok := strings.Cut(strings.TrimSpace(key), ":")
	if !ok || id == "" || secret == "" {
		return nil, fmt.Errorf("%w: the key of machine provider %s must be ACCESS_KEY_ID:SECRET_ACCESS_KEY",
			orchestration.ErrInvalid, rec.Name)
	}

	p, err := parseAWSOptions(rec.Options)
	if err != nil {
		return nil, err
	}

	creds := credentials.NewStaticCredentialsProvider(id, secret, "")
	p.providerID = rec.ID
	p.client = func(region string) ec2API {
		return ec2.New(ec2.Options{Region: region, Credentials: creds})
	}

	return p, nil
}

func parseAWSOptions(options map[string]string) (*awsProvider, error) {
	for name := range options {
		if !slices.Contains(awsOptions, name) {
			return nil, fmt.Errorf("%w: aws takes no option %q; it takes %s", orchestration.ErrInvalid, name,
				strings.Join(awsOptions, ", "))
		}
	}

	p := &awsProvider{subnet: options[AWSOptionSubnet], keyName: options[AWSOptionKeyName], diskGiB: awsDiskGiBDefault}

	for g := range strings.SplitSeq(options[AWSOptionSecurityGroups], ",") {
		if g = strings.TrimSpace(g); g != "" {
			p.groups = append(p.groups, g)
		}
	}

	if v := options[AWSOptionDiskGiB]; v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil || n < 8 || n > 16384 {
			return nil, fmt.Errorf("%w: %s must be a number of GiB from 8 to 16384", orchestration.ErrInvalid,
				AWSOptionDiskGiB)
		}

		p.diskGiB = int32(n)
	}

	return p, nil
}

func (p *awsProvider) Validate(ctx context.Context, region, size string) error {
	_, err := p.image(ctx, p.client(region), size)

	return err
}

// image returns the newest Ubuntu image for the size's architecture.
func (p *awsProvider) image(ctx context.Context, c ec2API, size string) (types.Image, error) {
	sizes, err := c.DescribeInstanceTypes(ctx, &ec2.DescribeInstanceTypesInput{
		InstanceTypes: []types.InstanceType{types.InstanceType(size)},
	})
	if err != nil {
		return types.Image{}, fmt.Errorf("reading instance type %s: %w", size, awsReason(err))
	}

	if len(sizes.InstanceTypes) == 0 || sizes.InstanceTypes[0].ProcessorInfo == nil {
		return types.Image{}, fmt.Errorf("instance type %s is not offered in this region", size)
	}

	arch, name := types.ArchitectureValuesX8664, "amd64"
	if slices.Contains(sizes.InstanceTypes[0].ProcessorInfo.SupportedArchitectures, types.ArchitectureTypeArm64) {
		arch, name = types.ArchitectureValuesArm64, "arm64"
	}

	images, err := c.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Owners: []string{canonicalOwner},
		Filters: []types.Filter{
			{Name: aws.String("name"), Values: []string{fmt.Sprintf(ubuntuImage, name)}},
			{Name: aws.String("architecture"), Values: []string{string(arch)}},
			{Name: aws.String("state"), Values: []string{string(types.ImageStateAvailable)}},
		},
	})
	if err != nil {
		return types.Image{}, fmt.Errorf("finding the Ubuntu image: %w", awsReason(err))
	}

	if len(images.Images) == 0 {
		return types.Image{}, errors.New("no Ubuntu 24.04 image is published in this region")
	}

	// CreationDate is an ISO 8601 time, so the newest sorts last as text.
	return slices.MaxFunc(images.Images, func(a, b types.Image) int {
		return strings.Compare(aws.ToString(a.CreationDate), aws.ToString(b.CreationDate))
	}), nil
}

func (p *awsProvider) Create(ctx context.Context, s Spec) (string, error) {
	c := p.client(s.Region)

	// A create that was cut short may have made the instance: use it rather than make a second one.
	if found, ok, err := p.find(ctx, c, s.NodeID); err != nil {
		return "", err
	} else if ok {
		return found.ID, nil
	}

	image, err := p.image(ctx, c, s.Size)
	if err != nil {
		return "", err
	}

	tags := []types.Tag{
		{Key: aws.String("Name"), Value: aws.String("hypomnema-" + s.Name)},
		{Key: aws.String(awsTagProvider), Value: aws.String(p.providerID)},
		{Key: aws.String(awsTagNode), Value: aws.String(s.NodeID)},
	}

	in := &ec2.RunInstancesInput{
		ImageId:      image.ImageId,
		InstanceType: types.InstanceType(s.Size),
		MinCount:     aws.Int32(1),
		MaxCount:     aws.Int32(1),
		UserData:     aws.String(base64.StdEncoding.EncodeToString([]byte(s.UserData))),
		// The same token makes a repeated call return the first call's instance.
		ClientToken: aws.String(s.RequestID),
		TagSpecifications: []types.TagSpecification{
			{ResourceType: types.ResourceTypeInstance, Tags: tags},
			{ResourceType: types.ResourceTypeVolume, Tags: tags},
		},
		// The user data holds the node's token: only IMDSv2, and only from the instance itself.
		MetadataOptions: &types.InstanceMetadataOptionsRequest{
			HttpEndpoint:            types.InstanceMetadataEndpointStateEnabled,
			HttpTokens:              types.HttpTokensStateRequired,
			HttpPutResponseHopLimit: aws.Int32(1),
		},
		BlockDeviceMappings: []types.BlockDeviceMapping{{
			DeviceName: image.RootDeviceName,
			Ebs: &types.EbsBlockDevice{
				VolumeSize: aws.Int32(p.diskGiB), VolumeType: types.VolumeTypeGp3, DeleteOnTermination: aws.Bool(true),
			},
		}},
		SecurityGroupIds: p.groups,
	}

	if p.subnet != "" {
		in.SubnetId = aws.String(p.subnet)
	}

	if p.keyName != "" {
		in.KeyName = aws.String(p.keyName)
	}

	out, err := c.RunInstances(ctx, in)
	if err != nil {
		return "", fmt.Errorf("creating the instance: %w", awsReason(err))
	}

	if len(out.Instances) != 1 {
		return "", fmt.Errorf("EC2 answered with %d instances, not one", len(out.Instances))
	}

	return aws.ToString(out.Instances[0].InstanceId), nil
}

func (p *awsProvider) Find(ctx context.Context, region, nodeID string) (Resource, bool, error) {
	return p.find(ctx, p.client(region), nodeID)
}

func (p *awsProvider) find(ctx context.Context, c ec2API, nodeID string) (Resource, bool, error) {
	found, err := p.list(ctx, c, types.Filter{Name: aws.String("tag:" + awsTagNode), Values: []string{nodeID}})
	if err != nil || len(found) == 0 {
		return Resource{}, false, err
	}

	return found[0], true, nil
}

func (p *awsProvider) Delete(ctx context.Context, region, nodeID string) error {
	c := p.client(region)

	found, err := p.list(ctx, c, types.Filter{Name: aws.String("tag:" + awsTagNode), Values: []string{nodeID}})
	if err != nil {
		return err
	}

	if len(found) == 0 {
		return nil
	}

	ids := make([]string, 0, len(found))
	for _, r := range found {
		ids = append(ids, r.ID)
	}

	if _, err := c.TerminateInstances(ctx, &ec2.TerminateInstancesInput{InstanceIds: ids}); err != nil {
		return fmt.Errorf("terminating %s: %w", strings.Join(ids, ", "), awsReason(err))
	}

	return nil
}

func (p *awsProvider) List(ctx context.Context, region string) ([]Resource, error) {
	return p.list(ctx, p.client(region))
}

// list returns this provider record's instances that are not already being
// destroyed, narrowed by more filters.
func (p *awsProvider) list(ctx context.Context, c ec2API, more ...types.Filter) ([]Resource, error) {
	in := &ec2.DescribeInstancesInput{Filters: append([]types.Filter{
		{Name: aws.String("tag:" + awsTagProvider), Values: []string{p.providerID}},
		{Name: aws.String("instance-state-name"), Values: []string{
			string(types.InstanceStateNamePending), string(types.InstanceStateNameRunning),
			string(types.InstanceStateNameStopping), string(types.InstanceStateNameStopped),
		}},
	}, more...)}

	var out []Resource

	for {
		page, err := c.DescribeInstances(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("listing instances: %w", awsReason(err))
		}

		for _, r := range page.Reservations {
			for _, i := range r.Instances {
				out = append(out, awsResource(i))
			}
		}

		if aws.ToString(page.NextToken) == "" {
			return out, nil
		}

		in.NextToken = page.NextToken
	}
}

// awsReason is AWS's own code and message for a refused call, without the
// SDK's operation, status and request id around them, which say nothing to
// the owner who reads it on the Machines page. Any other error is kept.
func awsReason(err error) error {
	var refused smithy.APIError
	if errors.As(err, &refused) {
		return fmt.Errorf("%s: %s", refused.ErrorCode(), refused.ErrorMessage())
	}

	return err
}

func awsResource(i types.Instance) Resource {
	r := Resource{ID: aws.ToString(i.InstanceId), Size: string(i.InstanceType), CreatedAt: aws.ToTime(i.LaunchTime).UTC()}

	if i.State != nil {
		r.State = string(i.State.Name)
	}

	if i.Placement != nil {
		r.Region = strings.TrimRightFunc(aws.ToString(i.Placement.AvailabilityZone), func(c rune) bool {
			return c >= 'a' && c <= 'z'
		})
	}

	for _, t := range i.Tags {
		if aws.ToString(t.Key) == awsTagNode {
			r.NodeID = aws.ToString(t.Value)
		}
	}

	return r
}
