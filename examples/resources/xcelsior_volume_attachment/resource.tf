resource "xcelsior_volume_attachment" "scratch" {
  volume_id   = xcelsior_volume.scratch.id
  instance_id = xcelsior_instance.train.id
  mount_path  = "/mnt/scratch"
  mode        = "rw"
}
