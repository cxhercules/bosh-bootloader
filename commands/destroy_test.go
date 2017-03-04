package commands_test

import (
	"bytes"
	"errors"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/cloudfoundry/bosh-bootloader/aws/cloudformation"
	"github.com/cloudfoundry/bosh-bootloader/bosh"
	"github.com/cloudfoundry/bosh-bootloader/commands"
	"github.com/cloudfoundry/bosh-bootloader/fakes"
	"github.com/cloudfoundry/bosh-bootloader/storage"
	"github.com/cloudfoundry/bosh-bootloader/terraform"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/ginkgo/extensions/table"
	. "github.com/onsi/gomega"
)

var _ = Describe("Destroy", func() {
	var (
		destroy                 commands.Destroy
		boshManager             *fakes.BOSHManager
		stackManager            *fakes.StackManager
		infrastructureManager   *fakes.InfrastructureManager
		vpcStatusChecker        *fakes.VPCStatusChecker
		stringGenerator         *fakes.StringGenerator
		logger                  *fakes.Logger
		awsKeyPairDeleter       *fakes.AWSKeyPairDeleter
		gcpKeyPairDeleter       *fakes.GCPKeyPairDeleter
		certificateDeleter      *fakes.CertificateDeleter
		credentialValidator     *fakes.CredentialValidator
		stateStore              *fakes.StateStore
		stateValidator          *fakes.StateValidator
		terraformExecutor       *fakes.TerraformExecutor
		terraformOutputProvider *fakes.TerraformOutputProvider
		networkInstancesChecker *fakes.NetworkInstancesChecker
		stdin                   *bytes.Buffer
	)

	BeforeEach(func() {
		stdin = bytes.NewBuffer([]byte{})
		logger = &fakes.Logger{}

		vpcStatusChecker = &fakes.VPCStatusChecker{}
		stackManager = &fakes.StackManager{}
		infrastructureManager = &fakes.InfrastructureManager{}
		boshManager = &fakes.BOSHManager{}
		awsKeyPairDeleter = &fakes.AWSKeyPairDeleter{}
		gcpKeyPairDeleter = &fakes.GCPKeyPairDeleter{}
		certificateDeleter = &fakes.CertificateDeleter{}
		stringGenerator = &fakes.StringGenerator{}
		credentialValidator = &fakes.CredentialValidator{}
		stateStore = &fakes.StateStore{}
		stateValidator = &fakes.StateValidator{}
		terraformExecutor = &fakes.TerraformExecutor{}
		terraformExecutor.VersionCall.Returns.Version = "0.8.7"
		networkInstancesChecker = &fakes.NetworkInstancesChecker{}

		terraformOutputProvider = &fakes.TerraformOutputProvider{}

		destroy = commands.NewDestroy(credentialValidator, logger, stdin, boshManager,
			vpcStatusChecker, stackManager, stringGenerator, infrastructureManager,
			awsKeyPairDeleter, gcpKeyPairDeleter, certificateDeleter, stateStore,
			stateValidator, terraformExecutor, terraformOutputProvider, networkInstancesChecker)
	})

	Describe("Execute", func() {
		It("returns when there is no state and --skip-if-missing flag is provided", func() {
			err := destroy.Execute([]string{"--skip-if-missing"}, storage.State{})

			Expect(err).NotTo(HaveOccurred())
			Expect(logger.StepCall.Receives.Message).To(Equal("state file not found, and --skip-if-missing flag provided, exiting"))
		})

		It("returns an error when state validator fails", func() {
			stateValidator.ValidateCall.Returns.Error = errors.New("state validator failed")
			err := destroy.Execute([]string{}, storage.State{})

			Expect(stateValidator.ValidateCall.CallCount).To(Equal(1))
			Expect(err).To(MatchError("state validator failed"))
		})

		DescribeTable("prompting the user for confirmation",
			func(response string, proceed bool) {
				fmt.Fprintf(stdin, "%s\n", response)

				err := destroy.Execute([]string{}, storage.State{
					BOSH: storage.BOSH{
						DirectorName: "some-director",
					},
					EnvID: "some-lake",
				})
				Expect(err).NotTo(HaveOccurred())

				Expect(logger.PromptCall.Receives.Message).To(Equal(`Are you sure you want to delete infrastructure for "some-lake"? This operation cannot be undone!`))

				if proceed {
					Expect(boshManager.DeleteCall.CallCount).To(Equal(1))
				} else {
					Expect(logger.StepCall.Receives.Message).To(Equal("exiting"))
					Expect(boshManager.DeleteCall.CallCount).To(Equal(0))
				}
			},
			Entry("responding with 'yes'", "yes", true),
			Entry("responding with 'y'", "y", true),
			Entry("responding with 'Yes'", "Yes", true),
			Entry("responding with 'Y'", "Y", true),
			Entry("responding with 'no'", "no", false),
			Entry("responding with 'n'", "n", false),
			Entry("responding with 'No'", "No", false),
			Entry("responding with 'N'", "N", false),
		)

		Context("when the --no-confirm flag is supplied", func() {
			DescribeTable("destroys without prompting the user for confirmation", func(flag string) {
				err := destroy.Execute([]string{flag}, storage.State{
					BOSH: storage.BOSH{
						DirectorName: "some-director",
					},
				})
				Expect(err).NotTo(HaveOccurred())

				Expect(logger.PromptCall.CallCount).To(Equal(0))
				Expect(boshManager.DeleteCall.CallCount).To(Equal(1))
			},
				Entry("--no-confirm", "--no-confirm"),
				Entry("-n", "-n"),
			)
		})

		It("invokes bosh delete", func() {
			stdin.Write([]byte("yes\n"))
			state := storage.State{
				BOSH: storage.BOSH{
					DirectorName: "some-director",
				},
			}

			err := destroy.Execute([]string{}, state)
			Expect(err).NotTo(HaveOccurred())

			Expect(boshManager.DeleteCall.CallCount).To(Equal(1))
			Expect(boshManager.DeleteCall.Receives.State).To(Equal(state))
		})

		It("clears the state", func() {
			stdin.Write([]byte("yes\n"))
			err := destroy.Execute([]string{}, storage.State{
				Stack: storage.Stack{
					Name:            "some-stack-name",
					LBType:          "some-lb-type",
					CertificateName: "some-certificate-name",
				},
			})

			Expect(err).NotTo(HaveOccurred())
			Expect(stateStore.SetCall.CallCount).To(Equal(3))
			Expect(stateStore.SetCall.Receives.State).To(Equal(storage.State{}))
		})

		Context("failure cases", func() {
			BeforeEach(func() {
				stdin.Write([]byte("yes\n"))
			})

			It("fast fails if the terraform installed is less than v0.8.5", func() {
				terraformExecutor.VersionCall.Returns.Version = "0.8.4"

				err := destroy.Execute([]string{}, storage.State{})

				Expect(err).To(MatchError("Terraform version must be at least v0.8.5"))
			})

			Context("when an invalid command line flag is supplied", func() {
				It("returns an error", func() {
					err := destroy.Execute([]string{"--invalid-flag"}, storage.State{})
					Expect(err).To(MatchError("flag provided but not defined: -invalid-flag"))
					Expect(credentialValidator.ValidateAWSCall.CallCount).To(Equal(0))
				})
			})

			Context("when bosh delete fails", func() {
				It("returns an error", func() {
					boshManager.DeleteCall.Returns.Error = errors.New("bosh delete-env failed")

					err := destroy.Execute([]string{}, storage.State{
						BOSH: storage.BOSH{
							DirectorName: "some-director",
						},
					})
					Expect(err).To(MatchError("bosh delete-env failed"))
				})
			})

			Context("when state store fails to set the state before destroying infrastructure", func() {
				It("returns an error", func() {
					stateStore.SetCall.Returns = []fakes.SetCallReturn{{errors.New("failed to set state")}}

					err := destroy.Execute([]string{}, storage.State{})
					Expect(err).To(MatchError("failed to set state"))
				})
			})

			Context("when state store fails to set the state before destroying certificate", func() {
				It("returns an error", func() {
					stateStore.SetCall.Returns = []fakes.SetCallReturn{{}, {errors.New("failed to set state")}}

					err := destroy.Execute([]string{}, storage.State{})
					Expect(err).To(MatchError("failed to set state"))
				})
			})

			Context("when the state fails to be set", func() {
				It("return an error", func() {
					stateStore.SetCall.Returns = []fakes.SetCallReturn{{}, {}, {errors.New("failed to set state")}}

					err := destroy.Execute([]string{}, storage.State{})
					Expect(err).To(MatchError("failed to set state"))
				})
			})
		})

		Context("when iaas is aws", func() {
			It("returns an error when aws credential validator fails", func() {
				credentialValidator.ValidateAWSCall.Returns.Error = errors.New("aws credentials validator failed")

				err := destroy.Execute([]string{}, storage.State{
					IAAS: "aws",
				})

				Expect(err).To(MatchError("aws credentials validator failed"))
			})

			Describe("destroying the aws infrastructure", func() {
				var (
					state storage.State
				)

				BeforeEach(func() {
					stdin.Write([]byte("yes\n"))
					state = storage.State{
						IAAS: "aws",
						AWS: storage.AWS{
							AccessKeyID:     "some-access-key-id",
							SecretAccessKey: "some-secret-access-key",
							Region:          "some-aws-region",
						},
						KeyPair: storage.KeyPair{
							Name:       "some-ec2-key-pair-name",
							PrivateKey: "some-private-key",
							PublicKey:  "some-public-key",
						},
						BOSH: storage.BOSH{
							DirectorUsername: "some-director-username",
							DirectorPassword: "some-director-password",
							State: map[string]interface{}{
								"key": "value",
							},
							Credentials: map[string]string{
								"some-username": "some-password",
							},
							DirectorSSLCertificate: "some-certificate",
							DirectorSSLPrivateKey:  "some-private-key",
						},
						Stack: storage.Stack{
							Name:            "some-stack-name",
							LBType:          "some-lb-type",
							CertificateName: "some-certificate-name",
						},
						EnvID: "bbl-lake-time:stamp",
					}
				})

				It("fails fast if BOSH deployed VMs still exist in the VPC", func() {
					stackManager.DescribeCall.Returns.Stack = cloudformation.Stack{
						Name:   "some-stack-name",
						Status: "some-stack-status",
						Outputs: map[string]string{
							"VPCID": "some-vpc-id",
						},
					}
					vpcStatusChecker.ValidateSafeToDeleteCall.Returns.Error = errors.New("vpc some-vpc-id is not safe to delete")

					err := destroy.Execute([]string{}, state)
					Expect(err).To(MatchError("vpc some-vpc-id is not safe to delete"))

					Expect(vpcStatusChecker.ValidateSafeToDeleteCall.Receives.VPCID).To(Equal("some-vpc-id"))
				})

				It("deletes the stack", func() {
					err := destroy.Execute([]string{}, state)
					Expect(err).NotTo(HaveOccurred())

					Expect(logger.StepCall.Messages).To(ContainElement("destroying AWS stack"))
					Expect(infrastructureManager.DeleteCall.Receives.StackName).To(Equal("some-stack-name"))
				})

				It("deletes the certificate", func() {
					err := destroy.Execute([]string{}, state)
					Expect(err).NotTo(HaveOccurred())

					Expect(certificateDeleter.DeleteCall.Receives.CertificateName).To(Equal("some-certificate-name"))
					Expect(logger.StepCall.Messages).To(ContainElement("deleting certificate"))
				})

				It("doesn't call delete certificate if there is no certificate to delete", func() {
					state.Stack.CertificateName = ""
					err := destroy.Execute([]string{}, state)
					Expect(err).NotTo(HaveOccurred())

					Expect(certificateDeleter.DeleteCall.CallCount).To(Equal(0))
				})

				It("deletes the keypair", func() {
					err := destroy.Execute([]string{}, state)
					Expect(err).NotTo(HaveOccurred())

					Expect(awsKeyPairDeleter.DeleteCall.Receives.Name).To(Equal("some-ec2-key-pair-name"))
				})

				It("logs the bosh deletion", func() {
					err := destroy.Execute([]string{}, state)
					Expect(err).NotTo(HaveOccurred())

					Expect(logger.StepCall.Messages).To(ContainElement("destroying bosh director"))
				})

				Context("reentrance", func() {
					Context("when the stack fails to delete", func() {
						It("removes the bosh properties from state and returns an error", func() {
							infrastructureManager.DeleteCall.Returns.Error = errors.New("failed to delete stack")

							err := destroy.Execute([]string{}, state)
							Expect(err).To(MatchError("failed to delete stack"))

							Expect(stateStore.SetCall.CallCount).To(Equal(1))
							Expect(stateStore.SetCall.Receives.State).To(Equal(storage.State{
								IAAS: "aws",
								AWS: storage.AWS{
									AccessKeyID:     "some-access-key-id",
									SecretAccessKey: "some-secret-access-key",
									Region:          "some-aws-region",
								},
								KeyPair: storage.KeyPair{
									Name:       "some-ec2-key-pair-name",
									PrivateKey: "some-private-key",
									PublicKey:  "some-public-key",
								},
								BOSH: storage.BOSH{},
								Stack: storage.Stack{
									Name:            "some-stack-name",
									LBType:          "some-lb-type",
									CertificateName: "some-certificate-name",
								},
								EnvID: "bbl-lake-time:stamp",
							}))
						})
					})

					Context("when there is no bosh to delete", func() {
						It("does not attempt to delete the bosh", func() {
							state.BOSH = storage.BOSH{}
							err := destroy.Execute([]string{}, state)
							Expect(err).NotTo(HaveOccurred())

							Expect(logger.PrintlnCall.Receives.Message).To(Equal("no BOSH director, skipping..."))
							Expect(logger.StepCall.Messages).NotTo(ContainElement("destroying bosh director"))
							Expect(boshManager.DeleteCall.CallCount).To(Equal(0))
						})
					})

					Context("when the certificate fails to delete", func() {
						It("removes the stack from the state and returns an error", func() {
							certificateDeleter.DeleteCall.Returns.Error = errors.New("failed to delete certificate")

							err := destroy.Execute([]string{}, state)
							Expect(err).To(MatchError("failed to delete certificate"))

							Expect(stateStore.SetCall.CallCount).To(Equal(2))
							Expect(stateStore.SetCall.Receives.State).To(Equal(storage.State{
								IAAS: "aws",
								AWS: storage.AWS{
									AccessKeyID:     "some-access-key-id",
									SecretAccessKey: "some-secret-access-key",
									Region:          "some-aws-region",
								},
								KeyPair: storage.KeyPair{
									Name:       "some-ec2-key-pair-name",
									PrivateKey: "some-private-key",
									PublicKey:  "some-public-key",
								},
								BOSH: storage.BOSH{},
								Stack: storage.Stack{
									Name:            "",
									LBType:          "",
									CertificateName: "some-certificate-name",
								},
								EnvID: "bbl-lake-time:stamp",
							}))
						})
					})

					Context("when the keypair fails to delete", func() {
						It("removes the certificate from the state and returns an error", func() {
							awsKeyPairDeleter.DeleteCall.Returns.Error = errors.New("failed to delete keypair")

							err := destroy.Execute([]string{}, state)
							Expect(err).To(MatchError("failed to delete keypair"))

							Expect(stateStore.SetCall.CallCount).To(Equal(3))
							Expect(stateStore.SetCall.Receives.State).To(Equal(storage.State{
								IAAS: "aws",
								AWS: storage.AWS{
									AccessKeyID:     "some-access-key-id",
									SecretAccessKey: "some-secret-access-key",
									Region:          "some-aws-region",
								},
								KeyPair: storage.KeyPair{
									Name:       "some-ec2-key-pair-name",
									PrivateKey: "some-private-key",
									PublicKey:  "some-public-key",
								},
								BOSH: storage.BOSH{},
								Stack: storage.Stack{
									Name:            "",
									LBType:          "",
									CertificateName: "",
								},
								EnvID: "bbl-lake-time:stamp",
							}))
						})
					})

					Context("when there is no stack to delete", func() {
						BeforeEach(func() {
							stackManager.DescribeCall.Returns.Error = cloudformation.StackNotFound
						})

						It("does not validate the vpc", func() {
							state.Stack = storage.Stack{}
							err := destroy.Execute([]string{}, state)
							Expect(err).NotTo(HaveOccurred())

							Expect(vpcStatusChecker.ValidateSafeToDeleteCall.CallCount).To(Equal(0))
						})

						It("does not attempt to delete the stack", func() {
							state.Stack = storage.Stack{}
							err := destroy.Execute([]string{}, state)
							Expect(err).NotTo(HaveOccurred())

							Expect(logger.PrintlnCall.Receives.Message).To(Equal("no AWS stack, skipping..."))
							Expect(infrastructureManager.DeleteCall.CallCount).To(Equal(0))
						})
					})
				})
			})

			Context("failure cases", func() {
				BeforeEach(func() {
					stdin.Write([]byte("yes\n"))
				})

				Context("when the stack manager cannot describe the stack", func() {
					It("returns an error", func() {
						stackManager.DescribeCall.Returns.Error = errors.New("cannot describe stack")

						err := destroy.Execute([]string{}, storage.State{
							IAAS: "aws",
						})
						Expect(err).To(MatchError("cannot describe stack"))
					})
				})

				Context("when failing to delete the stack", func() {
					It("returns an error", func() {
						infrastructureManager.DeleteCall.Returns.Error = errors.New("failed to delete stack")

						err := destroy.Execute([]string{}, storage.State{
							IAAS: "aws",
							Stack: storage.Stack{
								Name: "some-stack-name",
							},
						})
						Expect(err).To(MatchError("failed to delete stack"))
					})
				})

				Context("when the keypair cannot be deleted", func() {
					It("returns an error", func() {
						awsKeyPairDeleter.DeleteCall.Returns.Error = errors.New("failed to delete keypair")

						err := destroy.Execute([]string{}, storage.State{
							IAAS: "aws",
						})
						Expect(err).To(MatchError("failed to delete keypair"))
					})
				})

				Context("when the certificate cannot be deleted", func() {
					It("returns an error", func() {
						certificateDeleter.DeleteCall.Returns.Error = errors.New("failed to delete certificate")

						err := destroy.Execute([]string{}, storage.State{
							IAAS: "aws",
							Stack: storage.Stack{
								CertificateName: "some-certificate",
							}})
						Expect(err).To(MatchError("failed to delete certificate"))
					})
				})

				Context("when state store fails to set the state before destroying keypair", func() {
					It("returns an error", func() {
						stateStore.SetCall.Returns = []fakes.SetCallReturn{{}, {}, {errors.New("failed to set state")}}

						err := destroy.Execute([]string{}, storage.State{
							IAAS: "aws",
							Stack: storage.Stack{
								CertificateName: "some-certificate-name",
							},
						})
						Expect(err).To(MatchError("failed to set state"))
					})
				})

				Context("when bosh fails to delete the director", func() {
					var (
						state storage.State
					)
					BeforeEach(func() {
						state = storage.State{
							BOSH: storage.BOSH{
								State: map[string]interface{}{"hello": "world"},
							},
							IAAS: "aws",
							Stack: storage.Stack{
								CertificateName: "some-certificate-name",
							},
						}
					})
					Context("when bosh delete returns a bosh manager delete error", func() {
						var (
							errState storage.State
						)
						BeforeEach(func() {
							errState = storage.State{
								BOSH: storage.BOSH{
									State: map[string]interface{}{"error": "state"},
								},
								IAAS: "aws",
								Stack: storage.Stack{
									CertificateName: "some-certificate-name",
								},
							}
							boshManager.DeleteCall.Returns.Error = bosh.NewManagerDeleteError(errState, errors.New("deletion failed"))
						})

						It("saves the bosh state and returns an error", func() {
							err := destroy.Execute([]string{}, state)
							Expect(err).To(MatchError("deletion failed"))
							Expect(stateStore.SetCall.Receives.State).To(Equal(errState))
						})

						It("returns an error when it can't set the state", func() {
							stateStore.SetCall.Returns = []fakes.SetCallReturn{{
								errors.New("saving state failed"),
							}}
							err := destroy.Execute([]string{}, state)
							Expect(err).To(MatchError("the following errors occurred:\ndeletion failed,\nsaving state failed"))
						})
					})

					It("returns an error", func() {
						boshManager.DeleteCall.Returns.Error = errors.New("deletion failed")
						err := destroy.Execute([]string{}, state)
						Expect(err).To(MatchError("deletion failed"))
					})
				})
			})
		})

		Context("when iaas is gcp", func() {
			var serviceAccountKeyPath string
			var serviceAccountKey string
			BeforeEach(func() {
				terraformOutputProvider.GetCall.Returns.Outputs = terraform.Outputs{
					ExternalIP:      "some-external-ip",
					NetworkName:     "some-network-name",
					SubnetworkName:  "some-subnetwork-name",
					BOSHTag:         "some-bosh-tag-name",
					InternalTag:     "some-internal-tag-name",
					DirectorAddress: "some-director-address",
				}

				tempFile, err := ioutil.TempFile("", "gcpServiceAccountKey")
				Expect(err).NotTo(HaveOccurred())
				serviceAccountKeyPath = tempFile.Name()
				serviceAccountKey = `{"real": "json"}`
				err = ioutil.WriteFile(serviceAccountKeyPath, []byte(serviceAccountKey), os.ModePerm)
				Expect(err).NotTo(HaveOccurred())

			})

			It("returns an error when gcp credential validator fails", func() {
				credentialValidator.ValidateGCPCall.Returns.Error = errors.New("gcp credentials validator failed")

				err := destroy.Execute([]string{}, storage.State{
					IAAS: "gcp",
				})

				Expect(err).To(MatchError("gcp credentials validator failed"))
			})

			It("calls terraform destroy", func() {
				stdin.Write([]byte("yes\n"))
				err := destroy.Execute([]string{}, storage.State{
					IAAS:  "gcp",
					EnvID: "some-env-id",
					GCP: storage.GCP{
						ServiceAccountKey: "some-service-account-key",
						ProjectID:         "some-project-id",
						Zone:              "some-zone",
						Region:            "some-region",
					},
					TFState: "some-tf-state",
				})
				Expect(err).NotTo(HaveOccurred())

				Expect(terraformExecutor.DestroyCall.CallCount).To(Equal(1))
				Expect(terraformExecutor.DestroyCall.Receives.Credentials).To(Equal("some-service-account-key"))
				Expect(terraformExecutor.DestroyCall.Receives.EnvID).To(Equal("some-env-id"))
				Expect(terraformExecutor.DestroyCall.Receives.ProjectID).To(Equal("some-project-id"))
				Expect(terraformExecutor.DestroyCall.Receives.Zone).To(Equal("some-zone"))
				Expect(terraformExecutor.DestroyCall.Receives.Region).To(Equal("some-region"))
				Expect(terraformExecutor.DestroyCall.Receives.TFState).To(Equal("some-tf-state"))
				Expect(terraformExecutor.DestroyCall.Receives.Template).To(ContainSubstring(`variable "project_id"`))

				Expect(terraformExecutor.DestroyCall.Returns.TFState).To(Equal(""))
			})

			Context("when terraform destroy fails", func() {
				It("saves the partially destroyed tf state", func() {
					terraformExecutor.DestroyCall.Returns.Error = errors.New("failed to terraform destroy")
					terraformExecutor.DestroyCall.Returns.TFState = "some-tf-state"
					stdin.Write([]byte("yes\n"))
					err := destroy.Execute([]string{}, storage.State{
						IAAS:  "gcp",
						EnvID: "some-env-id",
						GCP: storage.GCP{
							ServiceAccountKey: "some-service-account-key",
							ProjectID:         "some-project-id",
							Zone:              "some-zone",
							Region:            "some-region",
						},
						TFState: "some-tf-state",
					})
					Expect(err).To(MatchError("failed to terraform destroy"))

					Expect(terraformExecutor.DestroyCall.CallCount).To(Equal(1))
					Expect(terraformExecutor.DestroyCall.Receives.Credentials).To(Equal("some-service-account-key"))
					Expect(terraformExecutor.DestroyCall.Receives.EnvID).To(Equal("some-env-id"))
					Expect(terraformExecutor.DestroyCall.Receives.ProjectID).To(Equal("some-project-id"))
					Expect(terraformExecutor.DestroyCall.Receives.Zone).To(Equal("some-zone"))
					Expect(terraformExecutor.DestroyCall.Receives.Region).To(Equal("some-region"))
					Expect(terraformExecutor.DestroyCall.Receives.TFState).To(Equal("some-tf-state"))
					Expect(terraformExecutor.DestroyCall.Receives.Template).To(ContainSubstring(`variable "project_id"`))

					Expect(stateStore.SetCall.Receives.State.TFState).To(Equal("some-tf-state"))
					Expect(stateStore.SetCall.CallCount).To(Equal(2))

				})
			})

			It("returns an error when instances exist in the gcp network", func() {
				networkInstancesChecker.ValidateSafeToDeleteCall.Returns.Error = errors.New("validation failed")

				projectID := "some-project-id"
				zone := "some-zone"
				tfState := "some-tf-state"
				err := destroy.Execute([]string{}, storage.State{
					IAAS:  "gcp",
					EnvID: "some-env-id",
					GCP: storage.GCP{
						ServiceAccountKey: "some-service-account-key",
						ProjectID:         projectID,
						Zone:              zone,
						Region:            "some-region",
					},
					TFState: tfState,
				})

				Expect(networkInstancesChecker.ValidateSafeToDeleteCall.Receives.NetworkName).To(Equal("some-network-name"))
				Expect(err).To(MatchError("validation failed"))
			})
		})

		It("deletes the keypair", func() {
			stdin.Write([]byte("yes\n"))
			err := destroy.Execute([]string{}, storage.State{
				IAAS: "gcp",
				KeyPair: storage.KeyPair{
					PublicKey: "some-public-key",
				},
				GCP: storage.GCP{
					ProjectID: "some-project-id",
				},
			})
			Expect(err).NotTo(HaveOccurred())

			Expect(gcpKeyPairDeleter.DeleteCall.CallCount).To(Equal(1))
			Expect(gcpKeyPairDeleter.DeleteCall.Receives.PublicKey).To(Equal("some-public-key"))
		})

		Context("failure cases", func() {
			It("returns an error when terraform executor fails to destroy", func() {
				stdin.Write([]byte("yes\n"))
				terraformExecutor.DestroyCall.Returns.Error = errors.New("failed to destroy")
				err := destroy.Execute([]string{}, storage.State{
					IAAS: "gcp",
				})

				Expect(err).To(MatchError("failed to destroy"))
			})

			It("returns an error when terraform executor fails to destroy and the resulting state fails to be set", func() {
				stdin.Write([]byte("yes\n"))
				stateStore.SetCall.Returns = []fakes.SetCallReturn{{}, {errors.New("failed to set state")}}
				terraformExecutor.DestroyCall.Returns.Error = errors.New("failed to destroy")
				err := destroy.Execute([]string{}, storage.State{
					IAAS: "gcp",
				})

				Expect(err).To(MatchError("the following errors occurred:\nfailed to destroy,\nfailed to set state"))
			})

			It("returns an error when the key pair deleter fails", func() {
				stdin.Write([]byte("yes\n"))
				gcpKeyPairDeleter.DeleteCall.Returns.Error = errors.New("failed to destroy")
				err := destroy.Execute([]string{}, storage.State{
					IAAS: "gcp",
				})

				Expect(err).To(MatchError("failed to destroy"))
			})

			It("returns an error when terraform output provider fails", func() {
				terraformOutputProvider.GetCall.Returns.Error = errors.New("terraform output provider failed")

				err := destroy.Execute([]string{}, storage.State{
					IAAS: "gcp",
				})

				Expect(err).To(MatchError("terraform output provider failed"))
			})
		})
	})
})
