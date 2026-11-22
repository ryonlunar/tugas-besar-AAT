# Klaster pengujian di Hetzner

Konfigurasi ini menjalankan empat node K3s dan satu VM k6 di Helsinki. Private network menghubungkan kelima VM. Firewall membuka SSH hanya untuk IP yang diberikan saat membuat plan.

## Membuat dan menghapus klaster

Siapkan OpenTofu, Python 3, Git, OpenSSH, serta API token Hetzner Cloud dengan akses Read & Write. Simpan token dalam file berizin `600`. Script membaca `~/.config/aat-hetzner/token` secara default. Gunakan `AAT_HCLOUD_TOKEN_FILE` untuk lokasi lain.

Dari root repository, ganti `IP_PUBLIK_ANDA/32` dengan IPv4 publik mesin yang digunakan untuk SSH.

```sh
infra/hetzner/cloud.sh plan p2-demo IP_PUBLIK_ANDA/32 HEAD
infra/hetzner/cloud.sh up p2-demo
```

`plan` mempersiapkan arsip dari Git ref dan menampilkan perubahan resource. `up` menerapkan plan berbayar dan menjalankan deployment. State dan log tersimpan di `.state/hetzner/p2-demo/`. Gunakan nama baru untuk setiap sesi.

Periksa klaster dari control plane.

```sh
infra/hetzner/cloud.sh ssh p2-demo aat-dev \
  "runuser -l ubuntu -c 'cd ~/aat && KUBECONFIG=~/.kube/aat-dev.yaml python3 scripts/m2/check-cluster.py'"
```

Salin profil pengujian ke VM `aat-loadgen` dan buka shell di VM tersebut.

```sh
for file in m2-p2.js run-m2-p2.py; do
  infra/hetzner/cloud.sh ssh p2-demo aat-loadgen \
    "umask 077; cat > /root/aat-bench/$file" < "scripts/load/$file"
done
infra/hetzner/cloud.sh ssh p2-demo aat-loadgen
```

Di VM, masuk ke `/root/aat-bench` dan set `K6_BIN=/root/aat-bench/k6` dengan `export`. Gunakan Client API `http://10.77.0.11:30080` dan Auth `http://10.77.0.11:30084` saat mengikuti [langkah pengujian P2](../../scripts/README.md#load-test-m2-p2). Secret Field Team untuk klaster ini tersimpan di `/home/ubuntu/aat/.state/m2/credentials.json` pada control plane.

Setelah menyimpan hasil, hapus resource dari sesi tersebut. Perintah yang sama berlaku bila provisioning selesai sebagian.

```sh
infra/hetzner/cloud.sh down p2-demo
```
