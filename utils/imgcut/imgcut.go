package imgcut

import (
	"github.com/disintegration/imaging"
	"image"
	"qq_bot/utils/zap"
)

func ImgCut(filepath string) error {
	img, err := imaging.Open(filepath)
	if err != nil {
		return err
	}

	// 2. 裁剪最外圈像素
	bounds := img.Bounds()
	croppedImg := imaging.Crop(img, image.Rect(
		1, 1, // 左上角坐标
		bounds.Dx()-1, // 宽度减1
		bounds.Dy()-1, // 高度减1
	))
	err = imaging.Save(croppedImg, filepath)
	if err != nil {
		return err
	}
	// 3. 保存结果
	zap.Logger.Debugf("图片%s裁切成功", filepath)
	return nil
}
