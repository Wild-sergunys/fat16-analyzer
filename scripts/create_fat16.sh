#!/bin/bash

echo "=== Создание образа FAT16 ==="

IMAGE_NAME="fat16.img"
IMAGE_SIZE="64"
MOUNT_POINT="/mnt/fat16"

# Создаем образ
echo "Создаем образ размером ${IMAGE_SIZE}MB..."
dd if=/dev/zero of=$IMAGE_NAME bs=1M count=$IMAGE_SIZE status=progress

# Форматируем
echo "Форматируем в FAT16..."
sudo mkfs.fat -F16 -n "TESTFAT16" -v $IMAGE_NAME

# Монтируем
echo "Монтируем образ..."
sudo mkdir -p $MOUNT_POINT
sudo mount -o loop,uid=1000,gid=1000 $IMAGE_NAME $MOUNT_POINT

# Создаем 5 файлов размером 8-12 кластеров
echo "Создаем тестовые файлы..."
sudo sh -c "
cd $MOUNT_POINT

# Файл 1: 8 кластеров (16KB)
dd if=/dev/urandom of=FILE1.bin bs=1K count=16 2>/dev/null

# Файл 2: 9 кластеров (18KB)
dd if=/dev/urandom of=FILE2.bin bs=1K count=18 2>/dev/null

# Файл 3: 10 кластеров (20KB)
dd if=/dev/urandom of=FILE3.bin bs=1K count=20 2>/dev/null

# Файл 4: 11 кластеров (22KB)
dd if=/dev/urandom of=FILE4.bin bs=1K count=22 2>/dev/null

# Файл 5: 12 кластеров (24KB)
dd if=/dev/urandom of=FILE5.bin bs=1K count=24 2>/dev/null

# Показываем результат
echo \"\"
echo \"Созданные файлы:\"
ls -lah *.bin
echo \"\"
echo \"Размеры файлов:\"
du -sh *.bin
"

# Размонтируем
echo ""
echo "Размонтируем..."
sudo umount $MOUNT_POINT

# Проверяем
echo "Проверяем ФС..."
sudo fsck.fat -v $IMAGE_NAME

echo ""
echo "=== Образ создан: $IMAGE_NAME ==="
echo ""
echo "Создано 5 файлов:"
echo "  FILE1.bin - 16KB (8 кластеров)"
echo "  FILE2.bin - 18KB (9 кластеров)"
echo "  FILE3.bin - 20KB (10 кластеров)"
echo "  FILE4.bin - 22KB (11 кластеров)"
echo "  FILE5.bin - 24KB (12 кластеров)"
