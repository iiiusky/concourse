package builder_test

import (
	"github.com/concourse/concourse/atc/builds"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/dbfakes"
	"github.com/concourse/concourse/atc/engine/builder"
	"github.com/concourse/concourse/atc/engine/builder/builderfakes"
	"github.com/concourse/concourse/atc/exec"
)

type StepBuilder interface {
	BuildStepper(db.Build) (exec.Stepper, error)
}

var _ = Describe("Builder", func() {

	Describe("BuildStep", func() {

		var (
			err error

			fakeStepFactory   *builderfakes.FakeStepFactory
			fakeRateLimiter   *builderfakes.FakeRateLimiter
			fakePolicyChecker *builderfakes.FakePolicyChecker

			planFactory atc.PlanFactory
			stepBuilder StepBuilder
		)

		BeforeEach(func() {
			fakeStepFactory = new(builderfakes.FakeStepFactory)
			fakeRateLimiter = new(builderfakes.FakeRateLimiter)
			fakePolicyChecker = new(builderfakes.FakePolicyChecker)

			stepBuilder = builder.NewStepBuilder(
				fakeStepFactory,
				"http://example.com",
				fakeRateLimiter,
				fakePolicyChecker,
			)

			planFactory = atc.NewPlanFactory(123)
		})

		Context("with a build", func() {
			var (
				fakeBuild    *dbfakes.FakeBuild
				fakePipeline *dbfakes.FakePipeline

				expectedPlan     atc.Plan
				expectedMetadata exec.StepMetadata
			)

			BeforeEach(func() {
				fakePipeline = new(dbfakes.FakePipeline)
				fakePipeline.IDReturns(2222)
				fakePipeline.NameReturns("some-pipeline")
				fakePipeline.InstanceVarsReturns(atc.InstanceVars{"branch": "master"})

				fakeBuild = new(dbfakes.FakeBuild)
				fakeBuild.IDReturns(4444)
				fakeBuild.NameReturns("42")
				fakeBuild.JobNameReturns("some-job")
				fakeBuild.JobIDReturns(3333)
				fakeBuild.PipelineIDReturns(fakePipeline.ID())
				fakeBuild.PipelineNameReturns(fakePipeline.Name())
				fakeBuild.PipelineInstanceVarsReturns(fakePipeline.InstanceVars())
				fakeBuild.PipelineReturns(fakePipeline, true, nil)
				fakeBuild.TeamNameReturns("some-team")
				fakeBuild.TeamIDReturns(1111)

				expectedMetadata = exec.StepMetadata{
					BuildID:              4444,
					BuildName:            "42",
					TeamID:               1111,
					TeamName:             "some-team",
					JobID:                3333,
					JobName:              "some-job",
					PipelineID:           2222,
					PipelineName:         "some-pipeline",
					PipelineInstanceVars: atc.InstanceVars{"branch": "master"},
					ExternalURL:          "http://example.com",
				}
			})

			Context("when the build has the wrong schema", func() {
				BeforeEach(func() {
					fakeBuild.SchemaReturns("not-schema")
				})

				It("errors", func() {
					_, err := stepBuilder.BuildStepper(fakeBuild)
					Expect(err).To(HaveOccurred())
				})
			})

			Context("when the build has the right schema", func() {
				BeforeEach(func() {
					fakeBuild.SchemaReturns("exec.v2")
				})

				JustBeforeEach(func() {
					fakeBuild.PrivatePlanReturns(expectedPlan)

					stepper, err := stepBuilder.BuildStepper(fakeBuild)
					Expect(err).ToNot(HaveOccurred())

					stepper(fakeBuild.PrivatePlan())
				})

				Context("with a putget in an aggregate", func() {
					var (
						putPlan               atc.Plan
						dependentGetPlan      atc.Plan
						otherPutPlan          atc.Plan
						otherDependentGetPlan atc.Plan
					)

					BeforeEach(func() {
						putPlan = planFactory.NewPlan(atc.PutPlan{
							Name:     "some-put",
							Resource: "some-output-resource",
							Type:     "put",
							Source:   atc.Source{"some": "source"},
							Params:   atc.Params{"some": "params"},
						})

						otherPutPlan = planFactory.NewPlan(atc.PutPlan{
							Name:     "some-put-2",
							Resource: "some-output-resource-2",
							Type:     "put",
							Source:   atc.Source{"some": "source-2"},
							Params:   atc.Params{"some": "params-2"},
						})

						expectedPlan = planFactory.NewPlan(atc.AggregatePlan{
							planFactory.NewPlan(atc.OnSuccessPlan{
								Step: putPlan,
								Next: dependentGetPlan,
							}),
							planFactory.NewPlan(atc.OnSuccessPlan{
								Step: otherPutPlan,
								Next: otherDependentGetPlan,
							}),
						})
					})

					Context("constructing outputs", func() {
						It("constructs the put correctly", func() {
							plan, stepMetadata, containerMetadata, _ := fakeStepFactory.PutStepArgsForCall(0)
							Expect(plan).To(Equal(putPlan))
							Expect(stepMetadata).To(Equal(expectedMetadata))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypePut,
								StepName:             "some-put",
								PipelineID:           2222,
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                3333,
								JobName:              "some-job",
								BuildID:              4444,
								BuildName:            "42",
							}))

							plan, stepMetadata, containerMetadata, _ = fakeStepFactory.PutStepArgsForCall(1)
							Expect(plan).To(Equal(otherPutPlan))
							Expect(stepMetadata).To(Equal(expectedMetadata))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypePut,
								StepName:             "some-put-2",
								PipelineID:           2222,
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                3333,
								JobName:              "some-job",
								BuildID:              4444,
								BuildName:            "42",
							}))
						})
					})
				})

				Context("with a putget in a parallel", func() {
					var (
						putPlan               atc.Plan
						dependentGetPlan      atc.Plan
						otherPutPlan          atc.Plan
						otherDependentGetPlan atc.Plan
					)

					BeforeEach(func() {
						putPlan = planFactory.NewPlan(atc.PutPlan{
							Name:     "some-put",
							Resource: "some-output-resource",
							Type:     "put",
							Source:   atc.Source{"some": "source"},
							Params:   atc.Params{"some": "params"},
						})

						otherPutPlan = planFactory.NewPlan(atc.PutPlan{
							Name:     "some-put-2",
							Resource: "some-output-resource-2",
							Type:     "put",
							Source:   atc.Source{"some": "source-2"},
							Params:   atc.Params{"some": "params-2"},
						})

						expectedPlan = planFactory.NewPlan(atc.InParallelPlan{
							Steps: []atc.Plan{
								planFactory.NewPlan(atc.OnSuccessPlan{
									Step: putPlan,
									Next: dependentGetPlan,
								}),
								planFactory.NewPlan(atc.OnSuccessPlan{
									Step: otherPutPlan,
									Next: otherDependentGetPlan,
								}),
							},
							Limit:    1,
							FailFast: true,
						})
					})

					Context("constructing outputs", func() {
						It("constructs the put correctly", func() {
							plan, stepMetadata, containerMetadata, _ := fakeStepFactory.PutStepArgsForCall(0)
							Expect(plan).To(Equal(putPlan))
							Expect(stepMetadata).To(Equal(expectedMetadata))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypePut,
								StepName:             "some-put",
								PipelineID:           2222,
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                3333,
								JobName:              "some-job",
								BuildID:              4444,
								BuildName:            "42",
							}))

							plan, stepMetadata, containerMetadata, _ = fakeStepFactory.PutStepArgsForCall(1)
							Expect(plan).To(Equal(otherPutPlan))
							Expect(stepMetadata).To(Equal(expectedMetadata))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypePut,
								StepName:             "some-put-2",
								PipelineID:           2222,
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                3333,
								JobName:              "some-job",
								BuildID:              4444,
								BuildName:            "42",
							}))
						})
					})
				})

				Context("with a retry plan", func() {
					var (
						getPlan       atc.Plan
						taskPlan      atc.Plan
						aggregatePlan atc.Plan
						parallelPlan  atc.Plan
						doPlan        atc.Plan
						timeoutPlan   atc.Plan
						retryPlanTwo  atc.Plan
					)

					BeforeEach(func() {
						getPlan = planFactory.NewPlan(atc.GetPlan{
							Name:     "some-get",
							Resource: "some-input-resource",
							Type:     "get",
							Source:   atc.Source{"some": "source"},
							Params:   atc.Params{"some": "params"},
						})

						taskPlan = planFactory.NewPlan(atc.TaskPlan{
							Name:       "some-task",
							Privileged: false,
							Tags:       atc.Tags{"some", "task", "tags"},
							ConfigPath: "some-config-path",
						})

						retryPlanTwo = planFactory.NewPlan(atc.RetryPlan{
							taskPlan,
							taskPlan,
						})

						aggregatePlan = planFactory.NewPlan(atc.AggregatePlan{retryPlanTwo})

						parallelPlan = planFactory.NewPlan(atc.InParallelPlan{
							Steps:    []atc.Plan{aggregatePlan},
							Limit:    1,
							FailFast: true,
						})

						doPlan = planFactory.NewPlan(atc.DoPlan{parallelPlan})

						timeoutPlan = planFactory.NewPlan(atc.TimeoutPlan{
							Step:     doPlan,
							Duration: "1m",
						})

						expectedPlan = planFactory.NewPlan(atc.RetryPlan{
							getPlan,
							timeoutPlan,
							getPlan,
						})
					})

					It("constructs the retry correctly", func() {
						Expect(*expectedPlan.Retry).To(HaveLen(3))
					})

					It("constructs the first get correctly", func() {
						plan, stepMetadata, containerMetadata, _ := fakeStepFactory.GetStepArgsForCall(0)
						expectedPlan := getPlan
						expectedPlan.Attempts = []int{1}
						Expect(plan).To(Equal(expectedPlan))
						Expect(stepMetadata).To(Equal(expectedMetadata))
						Expect(containerMetadata).To(Equal(db.ContainerMetadata{
							Type:                 db.ContainerTypeGet,
							StepName:             "some-get",
							PipelineID:           2222,
							PipelineName:         "some-pipeline",
							PipelineInstanceVars: "{\"branch\":\"master\"}",
							JobID:                3333,
							JobName:              "some-job",
							BuildID:              4444,
							BuildName:            "42",
							Attempt:              "1",
						}))
					})

					It("constructs the second get correctly", func() {
						plan, stepMetadata, containerMetadata, _ := fakeStepFactory.GetStepArgsForCall(1)
						expectedPlan := getPlan
						expectedPlan.Attempts = []int{3}
						Expect(plan).To(Equal(expectedPlan))
						Expect(stepMetadata).To(Equal(expectedMetadata))
						Expect(containerMetadata).To(Equal(db.ContainerMetadata{
							Type:                 db.ContainerTypeGet,
							StepName:             "some-get",
							PipelineID:           2222,
							PipelineName:         "some-pipeline",
							PipelineInstanceVars: "{\"branch\":\"master\"}",
							JobID:                3333,
							JobName:              "some-job",
							BuildID:              4444,
							BuildName:            "42",
							Attempt:              "3",
						}))
					})

					It("constructs nested retries correctly", func() {
						Expect(*retryPlanTwo.Retry).To(HaveLen(2))
					})

					It("constructs nested steps correctly", func() {
						plan, stepMetadata, containerMetadata, _ := fakeStepFactory.TaskStepArgsForCall(0)
						expectedPlan := taskPlan
						expectedPlan.Attempts = []int{2, 1}
						Expect(plan).To(Equal(expectedPlan))
						Expect(stepMetadata).To(Equal(expectedMetadata))
						Expect(containerMetadata).To(Equal(db.ContainerMetadata{
							Type:                 db.ContainerTypeTask,
							StepName:             "some-task",
							PipelineID:           2222,
							PipelineName:         "some-pipeline",
							PipelineInstanceVars: "{\"branch\":\"master\"}",
							JobID:                3333,
							JobName:              "some-job",
							BuildID:              4444,
							BuildName:            "42",
							Attempt:              "2.1",
						}))

						plan, stepMetadata, containerMetadata, _ = fakeStepFactory.TaskStepArgsForCall(1)
						expectedPlan = taskPlan
						expectedPlan.Attempts = []int{2, 2}
						Expect(plan).To(Equal(expectedPlan))
						Expect(stepMetadata).To(Equal(expectedMetadata))
						Expect(containerMetadata).To(Equal(db.ContainerMetadata{
							Type:                 db.ContainerTypeTask,
							StepName:             "some-task",
							PipelineID:           2222,
							PipelineName:         "some-pipeline",
							PipelineInstanceVars: "{\"branch\":\"master\"}",
							JobID:                3333,
							JobName:              "some-job",
							BuildID:              4444,
							BuildName:            "42",
							Attempt:              "2.2",
						}))
					})
				})

				Context("with a plan where conditional steps are inside retries", func() {
					var (
						onAbortPlan   atc.Plan
						onErrorPlan   atc.Plan
						onSuccessPlan atc.Plan
						onFailurePlan atc.Plan
						ensurePlan    atc.Plan
						leafPlan      atc.Plan
					)

					BeforeEach(func() {
						leafPlan = planFactory.NewPlan(atc.TaskPlan{
							Name:       "some-task",
							Privileged: false,
							Tags:       atc.Tags{"some", "task", "tags"},
							ConfigPath: "some-config-path",
						})

						onAbortPlan = planFactory.NewPlan(atc.OnAbortPlan{
							Step: leafPlan,
							Next: leafPlan,
						})

						onErrorPlan = planFactory.NewPlan(atc.OnErrorPlan{
							Step: onAbortPlan,
							Next: leafPlan,
						})

						onSuccessPlan = planFactory.NewPlan(atc.OnSuccessPlan{
							Step: onErrorPlan,
							Next: leafPlan,
						})

						onFailurePlan = planFactory.NewPlan(atc.OnFailurePlan{
							Step: onSuccessPlan,
							Next: leafPlan,
						})

						ensurePlan = planFactory.NewPlan(atc.EnsurePlan{
							Step: onFailurePlan,
							Next: leafPlan,
						})

						expectedPlan = planFactory.NewPlan(atc.RetryPlan{
							ensurePlan,
						})
					})

					It("constructs nested steps correctly", func() {
						Expect(fakeStepFactory.TaskStepCallCount()).To(Equal(6))

						_, _, containerMetadata, _ := fakeStepFactory.TaskStepArgsForCall(0)
						Expect(containerMetadata.Attempt).To(Equal("1"))
						_, _, containerMetadata, _ = fakeStepFactory.TaskStepArgsForCall(1)
						Expect(containerMetadata.Attempt).To(Equal("1"))
						_, _, containerMetadata, _ = fakeStepFactory.TaskStepArgsForCall(2)
						Expect(containerMetadata.Attempt).To(Equal("1"))
						_, _, containerMetadata, _ = fakeStepFactory.TaskStepArgsForCall(3)
						Expect(containerMetadata.Attempt).To(Equal("1"))
						_, _, containerMetadata, _ = fakeStepFactory.TaskStepArgsForCall(4)
						Expect(containerMetadata.Attempt).To(Equal("1"))
					})
				})

				Context("with a basic plan", func() {

					Context("that contains inputs", func() {
						BeforeEach(func() {
							expectedPlan = planFactory.NewPlan(atc.GetPlan{
								Name:     "some-input",
								Resource: "some-input-resource",
								Type:     "get",
								Tags:     []string{"some", "get", "tags"},
								Version:  &atc.Version{"some": "version"},
								Source:   atc.Source{"some": "source"},
								Params:   atc.Params{"some": "params"},
							})
						})

						It("constructs inputs correctly", func() {
							plan, stepMetadata, containerMetadata, _ := fakeStepFactory.GetStepArgsForCall(0)
							Expect(plan).To(Equal(expectedPlan))
							Expect(stepMetadata).To(Equal(expectedMetadata))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypeGet,
								StepName:             "some-input",
								PipelineID:           2222,
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                3333,
								JobName:              "some-job",
								BuildID:              4444,
								BuildName:            "42",
							}))
						})
					})

					Context("that contains tasks", func() {
						BeforeEach(func() {
							expectedPlan = planFactory.NewPlan(atc.TaskPlan{
								Name:          "some-task",
								ConfigPath:    "some-input/build.yml",
								InputMapping:  map[string]string{"foo": "bar"},
								OutputMapping: map[string]string{"baz": "qux"},
							})
						})

						It("constructs tasks correctly", func() {
							plan, stepMetadata, containerMetadata, _ := fakeStepFactory.TaskStepArgsForCall(0)
							Expect(plan).To(Equal(expectedPlan))
							Expect(stepMetadata).To(Equal(expectedMetadata))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypeTask,
								StepName:             "some-task",
								PipelineID:           2222,
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                3333,
								JobName:              "some-job",
								BuildID:              4444,
								BuildName:            "42",
							}))
						})
					})

					Context("that contains a set_pipeline step", func() {
						BeforeEach(func() {
							expectedPlan = planFactory.NewPlan(atc.SetPipelinePlan{
								Name:     "some-pipeline",
								File:     "some-input/pipeline.yml",
								VarFiles: []string{"foo", "bar"},
								Vars:     map[string]interface{}{"baz": "qux"},
							})
						})

						It("constructs set_pipeline correctly", func() {
							plan, stepMetadata, _ := fakeStepFactory.SetPipelineStepArgsForCall(0)
							Expect(plan).To(Equal(expectedPlan))
							Expect(stepMetadata).To(Equal(expectedMetadata))
						})
					})

					Context("that contains a load_var step", func() {
						BeforeEach(func() {
							expectedPlan = planFactory.NewPlan(atc.LoadVarPlan{
								Name: "some-var",
								File: "some-input/data.yml",
							})
						})

						It("constructs load_var correctly", func() {
							plan, stepMetadata, _ := fakeStepFactory.LoadVarStepArgsForCall(0)
							Expect(plan).To(Equal(expectedPlan))
							Expect(stepMetadata).To(Equal(expectedMetadata))
						})
					})

					Context("that contains a check step", func() {
						BeforeEach(func() {
							expectedPlan = planFactory.NewPlan(atc.CheckPlan{
								Name: "some-check",
							})
						})

						It("constructs the step correctly", func() {
							plan, stepMetadata, containerMetadata, _ := fakeStepFactory.CheckStepArgsForCall(0)
							Expect(plan).To(Equal(expectedPlan))
							Expect(stepMetadata).To(Equal(expectedMetadata))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypeCheck,
								StepName:             "some-check",
								PipelineID:           2222,
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: `{"branch":"master"}`,
								JobID:                3333,
								JobName:              "some-job",
								BuildID:              4444,
								BuildName:            "42",
							}))
						})
					})

					Context("that contains outputs", func() {
						var (
							putPlan          atc.Plan
							dependentGetPlan atc.Plan
						)

						BeforeEach(func() {
							putPlan = planFactory.NewPlan(atc.PutPlan{
								Name:     "some-put",
								Resource: "some-output-resource",
								Tags:     []string{"some", "putget", "tags"},
								Type:     "put",
								Source:   atc.Source{"some": "source"},
								Params:   atc.Params{"some": "params"},
							})

							dependentGetPlan = planFactory.NewPlan(atc.GetPlan{
								Name:        "some-get",
								Resource:    "some-input-resource",
								Tags:        []string{"some", "putget", "tags"},
								Type:        "get",
								VersionFrom: &putPlan.ID,
								Source:      atc.Source{"some": "source"},
								Params:      atc.Params{"another": "params"},
							})

							expectedPlan = planFactory.NewPlan(atc.OnSuccessPlan{
								Step: putPlan,
								Next: dependentGetPlan,
							})
						})

						It("constructs the put correctly", func() {
							plan, stepMetadata, containerMetadata, _ := fakeStepFactory.PutStepArgsForCall(0)
							Expect(plan).To(Equal(putPlan))
							Expect(stepMetadata).To(Equal(expectedMetadata))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypePut,
								StepName:             "some-put",
								PipelineID:           2222,
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                3333,
								JobName:              "some-job",
								BuildID:              4444,
								BuildName:            "42",
							}))
						})

						It("constructs the dependent get correctly", func() {
							plan, stepMetadata, containerMetadata, _ := fakeStepFactory.GetStepArgsForCall(0)
							Expect(plan).To(Equal(dependentGetPlan))
							Expect(stepMetadata).To(Equal(expectedMetadata))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								Type:                 db.ContainerTypeGet,
								StepName:             "some-get",
								PipelineID:           2222,
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                3333,
								JobName:              "some-job",
								BuildID:              4444,
								BuildName:            "42",
							}))
						})
					})
				})

				Context("running hooked composes", func() {
					Context("with all the hooks", func() {
						var (
							inputPlan          atc.Plan
							failureTaskPlan    atc.Plan
							successTaskPlan    atc.Plan
							completionTaskPlan atc.Plan
							nextTaskPlan       atc.Plan
						)

						BeforeEach(func() {
							inputPlan = planFactory.NewPlan(atc.GetPlan{
								Name: "some-input",
							})
							failureTaskPlan = planFactory.NewPlan(atc.TaskPlan{
								Name:   "some-failure-task",
								Config: &atc.TaskConfig{},
							})
							successTaskPlan = planFactory.NewPlan(atc.TaskPlan{
								Name:   "some-success-task",
								Config: &atc.TaskConfig{},
							})
							completionTaskPlan = planFactory.NewPlan(atc.TaskPlan{
								Name:   "some-completion-task",
								Config: &atc.TaskConfig{},
							})
							nextTaskPlan = planFactory.NewPlan(atc.TaskPlan{
								Name:   "some-next-task",
								Config: &atc.TaskConfig{},
							})

							expectedPlan = planFactory.NewPlan(atc.OnSuccessPlan{
								Step: planFactory.NewPlan(atc.EnsurePlan{
									Step: planFactory.NewPlan(atc.OnSuccessPlan{
										Step: planFactory.NewPlan(atc.OnFailurePlan{
											Step: inputPlan,
											Next: failureTaskPlan,
										}),
										Next: successTaskPlan,
									}),
									Next: completionTaskPlan,
								}),
								Next: nextTaskPlan,
							})
						})

						It("constructs the step correctly", func() {
							Expect(fakeStepFactory.GetStepCallCount()).To(Equal(1))
							plan, stepMetadata, containerMetadata, _ := fakeStepFactory.GetStepArgsForCall(0)
							Expect(plan).To(Equal(inputPlan))
							Expect(stepMetadata).To(Equal(expectedMetadata))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								PipelineID:           2222,
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                3333,
								JobName:              "some-job",
								BuildID:              4444,
								BuildName:            "42",
								StepName:             "some-input",
								Type:                 db.ContainerTypeGet,
							}))
						})

						It("constructs the completion hook correctly", func() {
							Expect(fakeStepFactory.TaskStepCallCount()).To(Equal(4))
							plan, stepMetadata, containerMetadata, _ := fakeStepFactory.TaskStepArgsForCall(2)
							Expect(plan).To(Equal(completionTaskPlan))
							Expect(stepMetadata).To(Equal(expectedMetadata))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								PipelineID:           2222,
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                3333,
								JobName:              "some-job",
								BuildID:              4444,
								BuildName:            "42",
								StepName:             "some-completion-task",
								Type:                 db.ContainerTypeTask,
							}))
						})

						It("constructs the failure hook correctly", func() {
							Expect(fakeStepFactory.TaskStepCallCount()).To(Equal(4))
							plan, stepMetadata, containerMetadata, _ := fakeStepFactory.TaskStepArgsForCall(0)
							Expect(plan).To(Equal(failureTaskPlan))
							Expect(stepMetadata).To(Equal(expectedMetadata))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								PipelineID:           2222,
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                3333,
								JobName:              "some-job",
								BuildID:              4444,
								BuildName:            "42",
								StepName:             "some-failure-task",
								Type:                 db.ContainerTypeTask,
							}))
						})

						It("constructs the success hook correctly", func() {
							Expect(fakeStepFactory.TaskStepCallCount()).To(Equal(4))
							plan, stepMetadata, containerMetadata, _ := fakeStepFactory.TaskStepArgsForCall(1)
							Expect(plan).To(Equal(successTaskPlan))
							Expect(stepMetadata).To(Equal(expectedMetadata))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								PipelineID:           2222,
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                3333,
								JobName:              "some-job",
								BuildID:              4444,
								BuildName:            "42",
								StepName:             "some-success-task",
								Type:                 db.ContainerTypeTask,
							}))
						})

						It("constructs the next step correctly", func() {
							Expect(fakeStepFactory.TaskStepCallCount()).To(Equal(4))
							plan, stepMetadata, containerMetadata, _ := fakeStepFactory.TaskStepArgsForCall(3)
							Expect(plan).To(Equal(nextTaskPlan))
							Expect(stepMetadata).To(Equal(expectedMetadata))
							Expect(containerMetadata).To(Equal(db.ContainerMetadata{
								PipelineID:           2222,
								PipelineName:         "some-pipeline",
								PipelineInstanceVars: "{\"branch\":\"master\"}",
								JobID:                3333,
								JobName:              "some-job",
								BuildID:              4444,
								BuildName:            "42",
								StepName:             "some-next-task",
								Type:                 db.ContainerTypeTask,
							}))
						})
					})
				})

				Context("running try steps", func() {
					var inputPlan atc.Plan

					BeforeEach(func() {
						inputPlan = planFactory.NewPlan(atc.GetPlan{
							Name: "some-input",
						})

						expectedPlan = planFactory.NewPlan(atc.TryPlan{
							Step: inputPlan,
						})
					})

					It("constructs the step correctly", func() {
						Expect(fakeStepFactory.GetStepCallCount()).To(Equal(1))
						plan, stepMetadata, containerMetadata, _ := fakeStepFactory.GetStepArgsForCall(0)
						Expect(plan).To(Equal(inputPlan))
						Expect(stepMetadata).To(Equal(expectedMetadata))
						Expect(containerMetadata).To(Equal(db.ContainerMetadata{
							Type:                 db.ContainerTypeGet,
							StepName:             "some-input",
							PipelineID:           2222,
							PipelineName:         "some-pipeline",
							PipelineInstanceVars: "{\"branch\":\"master\"}",
							JobID:                3333,
							JobName:              "some-job",
							BuildID:              4444,
							BuildName:            "42",
						}))
					})
				})

				Context("running across steps", func() {
					BeforeEach(func() {
						planner := builds.NewPlanner(planFactory)

						step := &atc.AcrossStep{
							Step: &atc.TaskStep{Name: "some-task"},
							Vars: []atc.AcrossVarConfig{
								{
									Var:    "var1",
									Values: []interface{}{"a1", "a2"},
								},
								{
									Var:    "var2",
									Values: []interface{}{"b1", "b2"},
								},
								{
									Var:    "var3",
									Values: []interface{}{"c1"},
								},
							},
						}

						expectedPlan, err = planner.Create(step, nil, nil, nil)
						Expect(err).ToNot(HaveOccurred())
					})

					It("constructs the steps correctly", func() {
						Expect(fakeStepFactory.TaskStepCallCount()).To(Equal(4))
						plan, stepMetadata, containerMetadata, _ := fakeStepFactory.TaskStepArgsForCall(0)
						Expect(*plan.Task).To(Equal(atc.TaskPlan{Name: "some-task"}))
						Expect(stepMetadata).To(Equal(expectedMetadata))
						Expect(containerMetadata).To(Equal(db.ContainerMetadata{
							Type:                 db.ContainerTypeTask,
							StepName:             "some-task",
							PipelineID:           2222,
							PipelineName:         "some-pipeline",
							PipelineInstanceVars: "{\"branch\":\"master\"}",
							JobID:                3333,
							JobName:              "some-job",
							BuildID:              4444,
							BuildName:            "42",
						}))
					})
				})
			})
		})
	})
})
