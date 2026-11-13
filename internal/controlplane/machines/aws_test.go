package machines

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
)

// fakeEC2 is EC2 in one region: what it holds and what it was asked.
type fakeEC2 struct {
	archs      []types.ArchitectureType
	images     []types.Image
	instances  []types.Instance
	imageIn    *ec2.DescribeImagesInput
	listIn     []*ec2.DescribeInstancesInput
	runs       []*ec2.RunInstancesInput
	runErr     error
	terminated []string
	stopped    []string
	started    []string
}

func (f *fakeEC2) DescribeInstanceTypes(context.Context, *ec2.DescribeInstanceTypesInput, ...func(*ec2.Options),
) (*ec2.DescribeInstanceTypesOutput, error) {
	return &ec2.DescribeInstanceTypesOutput{InstanceTypes: []types.InstanceTypeInfo{
		{ProcessorInfo: &types.ProcessorInfo{SupportedArchitectures: f.archs}},
	}}, nil
}

func (f *fakeEC2) DescribeImages(_ context.Context, in *ec2.DescribeImagesInput, _ ...func(*ec2.Options),
) (*ec2.DescribeImagesOutput, error) {
	f.imageIn = in

	return &ec2.DescribeImagesOutput{Images: f.images}, nil
}

func (f *fakeEC2) RunInstances(_ context.Context, in *ec2.RunInstancesInput, _ ...func(*ec2.Options),
) (*ec2.RunInstancesOutput, error) {
	f.runs = append(f.runs, in)

	if f.runErr != nil {
		return nil, f.runErr
	}

	return &ec2.RunInstancesOutput{Instances: []types.Instance{{InstanceId: aws.String("i-new")}}}, nil
}

func (f *fakeEC2) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options),
) (*ec2.DescribeInstancesOutput, error) {
	f.listIn = append(f.listIn, in)

	return &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{Instances: f.instances}}}, nil
}

func (f *fakeEC2) TerminateInstances(_ context.Context, in *ec2.TerminateInstancesInput, _ ...func(*ec2.Options),
) (*ec2.TerminateInstancesOutput, error) {
	f.terminated = append(f.terminated, in.InstanceIds...)

	return &ec2.TerminateInstancesOutput{}, nil
}

func (f *fakeEC2) StopInstances(_ context.Context, in *ec2.StopInstancesInput, _ ...func(*ec2.Options),
) (*ec2.StopInstancesOutput, error) {
	f.stopped = append(f.stopped, in.InstanceIds...)

	return &ec2.StopInstancesOutput{}, nil
}

func (f *fakeEC2) StartInstances(_ context.Context, in *ec2.StartInstancesInput, _ ...func(*ec2.Options),
) (*ec2.StartInstancesOutput, error) {
	f.started = append(f.started, in.InstanceIds...)

	return &ec2.StartInstancesOutput{}, nil
}

func newTestAWS(t *testing.T, f *fakeEC2, options map[string]string) Provider {
	t.Helper()

	rec := orchestration.MachineProvider{ID: "mpr_1", Name: "aws", Options: options}

	creds, err := AWSKey(rec, "AKIAEXAMPLE:secret")
	require.NoError(t, err)

	p, err := NewAWS(rec, creds)
	require.NoError(t, err)

	impl, ok := p.(*awsProvider)
	require.True(t, ok)

	impl.client = func(string) ec2API { return f }

	return p
}

func filterValues(filters []types.Filter) map[string][]string {
	out := map[string][]string{}
	for _, f := range filters {
		out[aws.ToString(f.Name)] = f.Values
	}

	return out
}

func tagValues(tags []types.Tag) map[string]string {
	out := map[string]string{}
	for _, t := range tags {
		out[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}

	return out
}

var spec = Spec{
	NodeID: "nod_1", Name: "build-1", Region: "ap-southeast-1", Size: "t4g.medium",
	UserData: "#!/bin/sh\necho first boot\n", RequestID: "ntk_1",
}

// Scenario: a machine is created. EC2 is asked for exactly one instance of
// the newest Ubuntu image for the size's architecture, tagged so it is found
// again by its node, with the first-boot script, a request token that makes
// a repeat harmless, and metadata that only the instance itself can read,
// because the script holds the node's token.
func TestAWSCreateAsksForOneTaggedInstanceThatKeepsItsScriptToItself(t *testing.T) {
	t.Parallel()

	f := &fakeEC2{
		archs: []types.ArchitectureType{types.ArchitectureTypeArm64},
		images: []types.Image{
			{ImageId: aws.String("ami-old"), CreationDate: aws.String("2026-01-05T00:00:00.000Z")},
			{
				ImageId: aws.String("ami-new"), CreationDate: aws.String("2026-10-20T00:00:00.000Z"),
				RootDeviceName: aws.String("/dev/sda1"),
			},
		},
	}
	p := newTestAWS(t, f, map[string]string{
		AWSOptionSubnet: "subnet-1", AWSOptionSecurityGroups: "sg-1, sg-2", AWSOptionKeyName: "owner",
		AWSOptionDiskGiB: "80",
	})

	id, err := p.Create(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, "i-new", id)

	assert.Equal(t, []string{canonicalOwner}, f.imageIn.Owners)
	assert.Equal(t, []string{"ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-arm64-server-*"},
		filterValues(f.imageIn.Filters)["name"])

	require.Len(t, f.runs, 1)
	in := f.runs[0]

	assert.Equal(t, "ami-new", aws.ToString(in.ImageId))
	assert.Equal(t, types.InstanceType("t4g.medium"), in.InstanceType)
	assert.Equal(t, int32(1), aws.ToInt32(in.MinCount))
	assert.Equal(t, int32(1), aws.ToInt32(in.MaxCount))
	assert.Equal(t, "ntk_1", aws.ToString(in.ClientToken))

	script, err := base64.StdEncoding.DecodeString(aws.ToString(in.UserData))
	require.NoError(t, err)
	assert.Equal(t, spec.UserData, string(script))

	require.NotNil(t, in.MetadataOptions)
	assert.Equal(t, types.HttpTokensStateRequired, in.MetadataOptions.HttpTokens)
	assert.Equal(t, int32(1), aws.ToInt32(in.MetadataOptions.HttpPutResponseHopLimit))

	require.Len(t, in.TagSpecifications, 2)
	assert.Equal(t, types.ResourceTypeInstance, in.TagSpecifications[0].ResourceType)
	assert.Equal(t, map[string]string{
		"Name": "hypomnema-build-1", awsTagProvider: "mpr_1", awsTagNode: "nod_1",
	}, tagValues(in.TagSpecifications[0].Tags))

	require.Len(t, in.BlockDeviceMappings, 1)
	assert.Equal(t, "/dev/sda1", aws.ToString(in.BlockDeviceMappings[0].DeviceName))
	assert.Equal(t, int32(80), aws.ToInt32(in.BlockDeviceMappings[0].Ebs.VolumeSize))
	assert.True(t, aws.ToBool(in.BlockDeviceMappings[0].Ebs.DeleteOnTermination))

	assert.Equal(t, "subnet-1", aws.ToString(in.SubnetId))
	assert.Equal(t, []string{"sg-1", "sg-2"}, in.SecurityGroupIds)
	assert.Equal(t, "owner", aws.ToString(in.KeyName))
}

// Scenario: a create is repeated after its first try was cut short, and EC2
// already has the node's instance. That instance is returned and no second
// one is asked for.
func TestAWSCreateTakesUpAnInstanceItAlreadyMade(t *testing.T) {
	t.Parallel()

	f := &fakeEC2{instances: []types.Instance{{InstanceId: aws.String("i-first")}}}
	p := newTestAWS(t, f, nil)

	id, err := p.Create(t.Context(), spec)
	require.NoError(t, err)
	assert.Equal(t, "i-first", id)
	assert.Empty(t, f.runs)
}

// Scenario: AWS refuses the instance. The reason kept for the owner is AWS's
// code and message, not the SDK's operation, status and request id.
func TestAWSRefusalIsToldInAWSsWords(t *testing.T) {
	t.Parallel()

	f := &fakeEC2{
		images: []types.Image{{ImageId: aws.String("ami-1"), CreationDate: aws.String("2026-10-20T00:00:00.000Z")}},
		runErr: &smithy.OperationError{
			ServiceID: "EC2", OperationName: "RunInstances",
			Err: fmt.Errorf("https response error StatusCode: 400, RequestID: 5f1c, %w", &smithy.GenericAPIError{
				Code: "VcpuLimitExceeded", Message: "You have requested more vCPU capacity than your limit allows.",
			}),
		},
	}

	_, err := newTestAWS(t, f, nil).Create(t.Context(), spec)
	require.EqualError(t, err,
		"creating the instance: VcpuLimitExceeded: You have requested more vCPU capacity than your limit allows.")
}

// Scenario: a node's machine is destroyed. Only instances this provider
// record made for that node are looked for, so another control plane's
// instances in the same account are never touched; and a machine already
// gone is not an error.
func TestAWSDeleteTerminatesOnlyTheNodesInstances(t *testing.T) {
	t.Parallel()

	f := &fakeEC2{instances: []types.Instance{{InstanceId: aws.String("i-1")}}}
	p := newTestAWS(t, f, nil)

	require.NoError(t, p.Delete(t.Context(), "ap-southeast-1", "nod_1"))
	assert.Equal(t, []string{"i-1"}, f.terminated)

	require.Len(t, f.listIn, 1)
	filters := filterValues(f.listIn[0].Filters)
	assert.Equal(t, []string{"mpr_1"}, filters["tag:"+awsTagProvider])
	assert.Equal(t, []string{"nod_1"}, filters["tag:"+awsTagNode])
	assert.NotContains(t, filters["instance-state-name"], "terminated")

	f.instances, f.terminated = nil, nil
	require.NoError(t, p.Delete(t.Context(), "ap-southeast-1", "nod_1"))
	assert.Empty(t, f.terminated)
}

// Scenario: the provider's machines are listed for the comparison with the
// registry. Each comes back with the node it was made for and its region.
func TestAWSListReadsTheNodeAndRegionOfEachInstance(t *testing.T) {
	t.Parallel()

	f := &fakeEC2{instances: []types.Instance{{
		InstanceId: aws.String("i-1"), InstanceType: types.InstanceType("t3.medium"),
		State:     &types.InstanceState{Name: types.InstanceStateNameRunning},
		Placement: &types.Placement{AvailabilityZone: aws.String("ap-southeast-1b")},
		Tags:      []types.Tag{{Key: aws.String(awsTagNode), Value: aws.String("nod_9")}},
	}}}

	got, err := newTestAWS(t, f, nil).List(t.Context(), "ap-southeast-1")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, Resource{ID: "i-1", NodeID: "nod_9", Region: "ap-southeast-1", Size: "t3.medium", State: "running",
		CreatedAt: got[0].CreatedAt}, got[0])
}

// Scenario: the owner's key or options are not ones AWS can use. The
// provider is refused with what is expected, before any call.
func TestAWSRefusesAKeyOrOptionsItCannotUse(t *testing.T) {
	t.Parallel()

	rec := orchestration.MachineProvider{ID: "mpr_1", Name: "aws"}

	for _, key := range []string{"", "AKIAEXAMPLE", ":secret", "AKIAEXAMPLE:"} {
		_, err := AWSKey(rec, key)
		require.ErrorIs(t, err, orchestration.ErrInvalid, key)
	}

	for _, options := range []map[string]string{
		{"vpc": "vpc-1"}, {AWSOptionDiskGiB: "huge"}, {AWSOptionDiskGiB: "2"},
	} {
		require.ErrorIs(t, ValidateAWSOptions(options), orchestration.ErrInvalid, options)
	}
}

// Scenario: a node's machine is stopped and started. EC2 is asked about the
// node's instance, found by its tags as for a removal; and a node that has
// no instance is an error, since nothing would be turned off or on.
func TestAWSStopsAndStartsTheNodesInstance(t *testing.T) {
	t.Parallel()

	f := &fakeEC2{instances: []types.Instance{{InstanceId: aws.String("i-1")}}}
	p := newTestAWS(t, f, nil)

	require.NoError(t, p.Stop(t.Context(), "ap-southeast-1", "nod_1"))
	require.NoError(t, p.Start(t.Context(), "ap-southeast-1", "nod_1"))
	assert.Equal(t, []string{"i-1"}, f.stopped)
	assert.Equal(t, []string{"i-1"}, f.started)
	assert.Equal(t, []string{"nod_1"}, filterValues(f.listIn[0].Filters)["tag:"+awsTagNode])
	assert.Contains(t, filterValues(f.listIn[1].Filters)["instance-state-name"], "stopped",
		"a stopped instance must be found to be started")

	f.instances = nil
	require.ErrorContains(t, p.Stop(t.Context(), "ap-southeast-1", "nod_1"), "no machine for this node")
	require.ErrorContains(t, p.Start(t.Context(), "ap-southeast-1", "nod_1"), "no machine for this node")
}
