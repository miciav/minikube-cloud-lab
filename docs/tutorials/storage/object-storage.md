# Store objects with Garage

From the checkout, run `source scripts/configure.sh`. Start this exercise with an empty lab or use your own object key in the existing course bucket. The client runs AWS CLI in a temporary container and accepts stdin.

```sh
lab install object-storage
printf 'hello from the lab\n' | lab client object-storage s3 cp - s3://course-files/hello
lab client object-storage s3 ls s3://course-files/
lab client object-storage --read-only s3 cp s3://course-files/hello -
printf 'denied' | lab client object-storage --read-only s3 cp - s3://course-files/forbidden
```

Expect the last upload to fail with AccessDenied. Run `lab uninstall object-storage`, reinstall it and repeat the read. A host SDK uses path-style addressing, region `garage`, endpoint `http://127.0.0.1:3900` and the access ID and secret on the two lines of the private student file in the directory printed by install. Garage implements a subset of the S3 API.

When finished, remove your object with `lab client object-storage s3 rm s3://course-files/hello` and uninstall the module.
