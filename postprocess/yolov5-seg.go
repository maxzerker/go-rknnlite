package postprocess

import (
	"github.com/swdee/go-rknnlite"
	"github.com/swdee/go-rknnlite/postprocess/result"
	"github.com/swdee/go-rknnlite/preprocess"
	"github.com/swdee/go-rknnlite/tracker"
)

// YOLOv5Seg defines the struct for YOLOv5Seg model inference post processing
type YOLOv5Seg struct {
	// Params are the Model configuration parameters
	Params YOLOv5SegParams
	// nextID is a counter that increments and provides the next number
	// for each detection result ID
	idGen *result.IDGenerator
	// protoSize is the Prototype tensor size of the Segment Mask
	protoSize int
	// buffer pools to stop allocation contention
	bufPool *bufferPool
	// bufPoolInit is a flag to indicate if the buffer pool has been initialized
	bufPoolInit bool
}

// YOLOv5SegParams defines the struct containing the YOLOv5Seg parameters to use
// for post processing operations
type YOLOv5SegParams struct {
	// Strides
	Strides []YOLOStride
	// BoxThreshold is the minimum probability score required for a bounding box
	// region to be considered for processing
	BoxThreshold float32
	// NMSThreshold is the Non-Maximum Suppression threshold used for defining
	// the maximum allowed Intersection Over Union (IoU) between two
	// bounding boxes for both to be kept
	NMSThreshold float32
	// ObjectClassNum is the number of different object classes the Model has
	// been trained with
	ObjectClassNum int
	// ProbBoxSize is the length of array elements representing each bounding
	// box's attributes.  Which represents the bounding box attributes plus
	// number of objects (ObjectClassNum) the Model was trained with
	ProbBoxSize int
	// MaxObjectNumber is the maximum number of objects detected that can be
	// returned
	MaxObjectNumber int
	// PrototypeChannel is the Prototype tensor defined in the Model used
	// for generating the Segment Mask.  This is the number of channels
	// generated
	PrototypeChannel int
	// PrototypeChannel is the Prototype tensor defined in the Model used
	// for generating the Segment Mask.  This is spatial resolution height
	PrototypeHeight int
	// PrototypeChannel is the Prototype tensor defined in the Model used
	// for generating the Segment Mask.  This is the spatial resolution weight
	PrototypeWeight int
}

// SegMask defines the segment mask data that is returned with detection results
type SegMask struct {
	// Mask is the segment mask data
	Mask []uint8
}

// YOLOv5SegDefaultParams returns an instance of YOLOv5SegParams configured with
// default values for a Model trained on the COCO dataset featuring:
// - Object Classes: 80
// - Anchor Boxes for each Stride of:
//   - Stride 8: (10x13), (16x30), (33x23)
//   - Stride 16: (30x61), (62x45), (59x119)
//   - Stride 32: (116x90), (156x198), (373x326)
//
// - Box Threshold: 0.25
// - NMS Threshold: 0.45
// - Prob Box Size: 85
//   - This is 80 Object Classes plus the 5 attributes used to define a bounding
//     box being:
//   - x & y coordinates for the center of the bounding box
//   - width and height of the box relative to whole image
//   - confidence score
//
// - Maximum Object Number: 64
// - PrototypeChannel: 32
// - PrototypeHeight: 160
// - PrototypeWeight: 160
func YOLOv5SegCOCOParams() YOLOv5SegParams {
	return YOLOv5SegParams{
		Strides: []YOLOStride{
			{
				Size:   8,
				Anchor: []int{10, 13, 16, 30, 33, 23},
			},
			{
				Size:   16,
				Anchor: []int{30, 61, 62, 45, 59, 119},
			},
			{
				Size:   32,
				Anchor: []int{116, 90, 156, 198, 373, 326},
			},
		},
		BoxThreshold:     0.25,
		NMSThreshold:     0.45,
		ObjectClassNum:   80,
		ProbBoxSize:      85,
		MaxObjectNumber:  64,
		PrototypeChannel: 32,
		PrototypeHeight:  160,
		PrototypeWeight:  160,
	}
}

// NewYOLOv5Seg returns an instance of the YOLOv5Seg post processor
func NewYOLOv5Seg(p YOLOv5SegParams) *YOLOv5Seg {
	return &YOLOv5Seg{
		Params:    p,
		idGen:     result.NewIDGenerator(),
		protoSize: p.PrototypeChannel * p.PrototypeHeight * p.PrototypeWeight,
		bufPool:   NewBufferPool(),
	}
}

// newStrideDataSeg returns an initialised instance of strideData
func newStrideDataSeg(outputs *rknnlite.Outputs, protoSize int) *strideData {

	in := outputs.InputAttributes()
	out := outputs.OutputAttributes()

	s := &strideData{
		filterBoxes:         make([]float32, 0),
		objProbs:            make([]float32, 0),
		classID:             make([]int, 0),
		outScales:           out.Scales,
		outZPs:              out.ZPs,
		height:              in.Height,
		width:               in.Width,
		filterSegments:      make([]float32, 0),
		filterSegmentsByNMS: make([]float32, 0),
		proto:               make([]float32, protoSize),
	}

	return s
}

// YOLOv5SegResult defines a struct used for the results of YOLO segmentation
// models
type YOLOv5SegResult struct {
	DetectResults []result.DetectResult
	SegmentData   SegmentData
}

// SegmentData defines a struct for storing segment data that was created
// during the object detection phase so we can process segment masks afterwards
type SegmentData struct {
	// filterBoxesByNMS stores boxes used to filer crop mask
	filterBoxesByNMS []int
	// data stores stride data
	data *strideData
	// number of boxes
	boxesNum int
}

// GetDetectResults returns the object detection results containing bounding
// boxes
func (r YOLOv5SegResult) GetDetectResults() []result.DetectResult {
	return r.DetectResults
}

func (r YOLOv5SegResult) GetSegmentData() SegmentData {
	return r.SegmentData
}

// DetectObjects takes the RKNN outputs and runs the object detection process
// then returns the results
func (y *YOLOv5Seg) DetectObjects(outputs *rknnlite.Outputs,
	resizer *preprocess.Resizer) result.DetectionResult {

	// strides in protoype code
	data := newStrideDataSeg(outputs, y.protoSize)

	validCount := 0

	// process outputs of rknn
	for i := 0; i < 7; i++ {

		// same as process_i8() in C code
		validCount += y.processStride(
			outputs,
			i,
			data,
		)
	}

	if validCount <= 0 {
		// no object detected
		return YOLOv5SegResult{}
	}

	// indexArray is used to keep and index of detect objects contained in
	// the stride "data" variable
	var indexArray []int

	for i := 0; i < validCount; i++ {
		indexArray = append(indexArray, i)
	}

	quickSortIndiceInverse(data.objProbs, 0, validCount-1, indexArray)

	// create a unique set of ClassID (ie: eliminate any multiples found)
	classSet := make(map[int]bool)

	for _, id := range data.classID {
		classSet[id] = true
	}

	// for each classID in the classSet calculate the NMS
	for c := range classSet {
		nms(validCount, data.filterBoxes, data.classID, indexArray, c,
			y.Params.NMSThreshold, 4)
	}

	// collate objects into a result for returning
	group := make([]result.DetectResult, 0)
	lastCount := 0

	for i := 0; i < validCount; i++ {
		if indexArray[i] == -1 || lastCount >= y.Params.MaxObjectNumber {
			continue
		}
		n := indexArray[i]

		x1 := data.filterBoxes[n*4+0]
		y1 := data.filterBoxes[n*4+1]
		x2 := x1 + data.filterBoxes[n*4+2]
		y2 := y1 + data.filterBoxes[n*4+3]
		id := data.classID[n]
		objConf := data.objProbs[i]

		for k := 0; k < y.Params.PrototypeChannel; k++ {
			data.filterSegmentsByNMS = append(data.filterSegmentsByNMS,
				data.filterSegments[n*y.Params.PrototypeChannel+k])
		}

		result := result.DetectResult{
			Box: result.BoxRect{
				// have left the clamps on here versus C code original
				Left:   int(clamp(x1, 0, data.width)),
				Top:    int(clamp(y1, 0, data.height)),
				Right:  int(clamp(x2, 0, data.width)),
				Bottom: int(clamp(y2, 0, data.height)),
			},
			Probability: objConf,
			Class:       id,
			ID:          y.idGen.GetNext(),
		}

		group = append(group, result)
		lastCount++
	}

	boxesNum := len(group)
	segData := SegmentData{
		filterBoxesByNMS: make([]int, boxesNum*4),
		data:             data,
		boxesNum:         boxesNum,
	}

	for i := 0; i < boxesNum; i++ {
		// store filter boxes at their original size for segment mask calculations
		segData.filterBoxesByNMS[i*4+0] = group[i].Box.Left
		segData.filterBoxesByNMS[i*4+1] = group[i].Box.Top
		segData.filterBoxesByNMS[i*4+2] = group[i].Box.Right
		segData.filterBoxesByNMS[i*4+3] = group[i].Box.Bottom

		// resize detection boxes back to that of original image
		group[i].Box.Left = boxReverse(group[i].Box.Left, resizer.XPad(), resizer.ScaleFactor())
		group[i].Box.Top = boxReverse(group[i].Box.Top, resizer.YPad(), resizer.ScaleFactor())
		group[i].Box.Right = boxReverse(group[i].Box.Right, resizer.XPad(), resizer.ScaleFactor())
		group[i].Box.Bottom = boxReverse(group[i].Box.Bottom, resizer.YPad(), resizer.ScaleFactor())
	}

	res := YOLOv5SegResult{
		DetectResults: group,
		SegmentData:   segData,
	}

	return res
}

// SegmentMask creates segment mask data for object detection results
func (y *YOLOv5Seg) SegmentMask(detectObjs result.DetectionResult,
	resizer *preprocess.Resizer) SegMask {

	// handle segment masks
	segData := detectObjs.(YOLOv5SegResult).GetSegmentData()
	boxesNum := segData.boxesNum
	modelH := int(segData.data.height)
	modelW := int(segData.data.width)

	y.initBufferPool(segData, resizer)

	// C code does not use USE_FP_RESIZE as uint8 is faster via CPU calculation
	// than using NPU

	// compute the mask through Matmul.  we have a parallel version of the code
	// which uses goroutines, but speed benefits are only gained from about
	// greater than 6 boxes. the parallel version has a negative consequence
	// in that it effects the performance of the resizeByOpenCVUint8() call
	// afterwards due to the overhead of the goroutines being cleaned up.
	matmulOut := y.bufPool.Get(bufMatMul,
		boxesNum*y.Params.PrototypeHeight*y.Params.PrototypeWeight)
	defer y.bufPool.Put(bufMatMul, matmulOut)

	if boxesNum > 6 {
		matmulUint8Parallel(
			segData.data, boxesNum,
			y.Params.PrototypeChannel,
			y.Params.PrototypeHeight,
			y.Params.PrototypeWeight,
			matmulOut,
		)
	} else {
		matmulUint8(
			segData.data, boxesNum,
			y.Params.PrototypeChannel,
			y.Params.PrototypeHeight,
			y.Params.PrototypeWeight,
			matmulOut,
		)
	}

	// resize each proto‑mask to full model input dims,
	// but only in its bounding‑box ROI, merging into allMaskInOne
	allMask := y.bufPool.Get(bufAllMask, modelH*modelW)
	defer y.bufPool.Put(bufAllMask, allMask)

	protoH := y.Params.PrototypeHeight
	protoW := y.Params.PrototypeWeight

	// temp buffer for one‑box resize
	segMaskBuf := y.bufPool.Get(bufSegMask, modelH*modelW)
	defer y.bufPool.Put(bufSegMask, segMaskBuf)

	for b := 0; b < boxesNum; b++ {
		// get the b'th proto mask slice
		start := b * protoH * protoW
		protoSlice := matmulOut[start : start+protoH*protoW]

		// resize that one box’s mask to the full model dims
		// not just ROI—so segReverse’s cropping lines up
		resizeByOpenCVUint8(
			protoSlice, protoW, protoH,
			1,
			segMaskBuf, modelW, modelH,
		)

		// merge just ROI pixels into allMask
		// filterBoxesByNMS is in model coords
		x1 := segData.filterBoxesByNMS[b*4+0]
		y1 := segData.filterBoxesByNMS[b*4+1]
		x2 := segData.filterBoxesByNMS[b*4+2]
		y2 := segData.filterBoxesByNMS[b*4+3]

		// clamp
		if x1 < 0 {
			x1 = 0
		}

		if y1 < 0 {
			y1 = 0
		}

		if x2 > modelW {
			x2 = modelW
		}

		if y2 > modelH {
			y2 = modelH
		}

		id := uint8(b + 1) // assign unique id to object

		for yy := y1; yy < y2; yy++ {
			base := yy*modelW + x1

			for xx := x1; xx < x2; xx++ {
				if segMaskBuf[yy*modelW+xx] != 0 {
					allMask[base+xx-x1] = id
				}
			}
		}
	}

	// do segReverse to produce the final real‑image mask
	croppedH := modelH - resizer.YPad()*2
	croppedW := modelW - resizer.XPad()*2
	realH := resizer.SrcHeight()
	realW := resizer.SrcWidth()

	cropBuf := y.bufPool.Get(bufCrop, croppedH*croppedW)
	defer y.bufPool.Put(bufCrop, cropBuf)

	// allocate final mask right before return
	realMask := make([]uint8, realH*realW)

	segReverse(
		allMask,  // model‑input mask
		cropBuf,  // temp cropped
		realMask, // output
		modelH, modelW,
		croppedH, croppedW,
		realH, realW,
		resizer.YPad(), resizer.XPad(),
	)

	return SegMask{realMask}
}

// TrackMask creates segment mask data for tracked objects
func (y *YOLOv5Seg) TrackMask(detectObjs result.DetectionResult,
	trackObjs []*tracker.STrack, resizer *preprocess.Resizer) SegMask {

	// handle segment masks
	detRes := detectObjs.(YOLOv5SegResult).GetDetectResults()
	segData := detectObjs.(YOLOv5SegResult).GetSegmentData()
	boxesNum := segData.boxesNum
	modelH := int(segData.data.height)
	modelW := int(segData.data.width)

	// the detection objects and tracked objects can be different, so we need
	// to adjust the segment mask to only have tracked object masks and strip
	// out the non-used ones
	keep := make([]bool, boxesNum)

	for _, to := range trackObjs {
		for i, dr := range detRes {
			if dr.ID == to.GetDetectionID() {
				keep[i] = true
				break
			}
		}
	}

	y.initBufferPool(segData, resizer)

	// C code does not use USE_FP_RESIZE as uint8 is faster via CPU calculation
	// than using NPU

	// compute the mask through Matmul.  we have a parallel version of the code
	// which uses goroutines, but speed benefits are only gained from about
	// greater than 6 boxes. the parallel version has a negative consequence
	// in that it effects the performance of the resizeByOpenCVUint8() call
	// afterwards due to the overhead of the goroutines being cleaned up.
	matmulOut := y.bufPool.Get(bufMatMul,
		boxesNum*y.Params.PrototypeHeight*y.Params.PrototypeWeight)
	defer y.bufPool.Put(bufMatMul, matmulOut)

	if boxesNum > 6 {
		matmulUint8Parallel(
			segData.data, boxesNum,
			y.Params.PrototypeChannel,
			y.Params.PrototypeHeight,
			y.Params.PrototypeWeight,
			matmulOut,
		)
	} else {
		matmulUint8(
			segData.data, boxesNum,
			y.Params.PrototypeChannel,
			y.Params.PrototypeHeight,
			y.Params.PrototypeWeight,
			matmulOut,
		)
	}

	// prepare combined mask at model resolution
	allMask := y.bufPool.Get(bufAllMask, modelH*modelW)
	defer y.bufPool.Put(bufAllMask, allMask)

	protoH := y.Params.PrototypeHeight
	protoW := y.Params.PrototypeWeight

	// temp buffer for per‑object resize
	segMaskBuf := y.bufPool.Get(bufSegMask, modelH*modelW)
	defer y.bufPool.Put(bufSegMask, segMaskBuf)

	for b := 0; b < boxesNum; b++ {
		// skip objects we are not tracking
		if !keep[b] {
			continue
		}

		// resize proto mask[b] to model dims
		start := b * protoH * protoW
		protoSlice := matmulOut[start : start+protoH*protoW]

		resizeByOpenCVUint8(
			protoSlice, protoW, protoH,
			1,
			segMaskBuf, modelW, modelH,
		)

		// merge only within ROI
		x1 := segData.filterBoxesByNMS[b*4+0]
		y1 := segData.filterBoxesByNMS[b*4+1]
		x2 := segData.filterBoxesByNMS[b*4+2]
		y2 := segData.filterBoxesByNMS[b*4+3]

		// clamp
		if x1 < 0 {
			x1 = 0
		}

		if y1 < 0 {
			y1 = 0
		}

		if x2 > modelW {
			x2 = modelW
		}

		if y2 > modelH {
			y2 = modelH
		}

		id := uint8(b + 1) // assign unique id to object

		for yy := y1; yy < y2; yy++ {
			base := yy*modelW + x1

			for xx := x1; xx < x2; xx++ {
				if segMaskBuf[yy*modelW+xx] != 0 {
					allMask[base+xx-x1] = id
				}
			}
		}
	}

	// reverse to original image size
	croppedH := modelH - resizer.YPad()*2
	croppedW := modelW - resizer.XPad()*2
	realH := resizer.SrcHeight()
	realW := resizer.SrcWidth()

	cropBuf := y.bufPool.Get(bufCrop, croppedH*croppedW)
	defer y.bufPool.Put(bufCrop, cropBuf)

	// allocate final mask right before return
	realMask := make([]uint8, realH*realW)

	segReverse(
		allMask,
		cropBuf,
		realMask,
		modelH, modelW,
		croppedH, croppedW,
		realH, realW,
		resizer.YPad(), resizer.XPad(),
	)

	return SegMask{realMask}
}

// processStride processes the given stride
func (y *YOLOv5Seg) processStride(outputs *rknnlite.Outputs, inputID int,
	data *strideData) int {

	gridH := int(outputs.OutputAttributes().DimHeights[inputID])
	gridW := int(outputs.OutputAttributes().DimWidths[inputID])
	stride := int(data.height) / gridH

	validCount := 0
	gridLen := gridH * gridW

	if inputID%2 == 1 {
		return validCount
	}

	if inputID == 6 {
		inputProto := outputs.Output[inputID].BufInt
		zpProto := data.outZPs[inputID]
		scaleProto := data.outScales[inputID]

		for i := 0; i < y.protoSize; i++ {
			data.proto[i] = deqntAffineToF32(inputProto[i], zpProto, scaleProto)
		}

		return validCount
	}

	input := outputs.Output[inputID].BufInt
	inputSeg := outputs.Output[inputID+1].BufInt
	zp := data.outZPs[inputID]
	scale := data.outScales[inputID]
	zpSeg := data.outZPs[inputID+1]
	scaleSeg := data.outScales[inputID+1]

	thresI8 := qntF32ToAffine(y.Params.BoxThreshold, zp, scale)

	for a := 0; a < 3; a++ {
		for i := 0; i < gridH; i++ {
			for j := 0; j < gridW; j++ {

				boxConfidence := input[(y.Params.ProbBoxSize*a+4)*gridLen+i*gridW+j]

				if boxConfidence >= thresI8 {

					offset := (y.Params.ProbBoxSize*a)*gridLen + i*gridW + j
					offsetSeg := (y.Params.PrototypeChannel*a)*gridLen + i*gridW + j
					inPtr := offset // Used as a starting index into input
					inPtrSeg := offsetSeg

					boxX := (deqntAffineToF32(input[inPtr], zp, scale))*2.0 - 0.5
					boxY := (deqntAffineToF32(input[inPtr+gridLen], zp, scale))*2.0 - 0.5
					boxW := (deqntAffineToF32(input[inPtr+2*gridLen], zp, scale)) * 2.0
					boxH := (deqntAffineToF32(input[inPtr+3*gridLen], zp, scale)) * 2.0

					boxX = (boxX + float32(j)) * float32(stride)
					boxY = (boxY + float32(i)) * float32(stride)
					boxW = boxW * boxW * float32(y.Params.Strides[inputID/2].Anchor[a*2])
					boxH = boxH * boxH * float32(y.Params.Strides[inputID/2].Anchor[a*2+1])
					boxX -= boxW / 2.0
					boxY -= boxH / 2.0

					maxClassProbs := input[inPtr+5*gridLen]
					maxClassID := 0

					for k := 1; k < y.Params.ObjectClassNum; k++ {
						prob := input[inPtr+(5+k)*gridLen]
						if prob > maxClassProbs {
							maxClassID = k
							maxClassProbs = prob
						}
					}

					boxConfF32 := deqntAffineToF32(boxConfidence, zp, scale)
					classProbF32 := deqntAffineToF32(maxClassProbs, zp, scale)
					limitScore := boxConfF32 * classProbF32

					if limitScore > y.Params.BoxThreshold {
						for k := 0; k < y.Params.PrototypeChannel; k++ {
							segElementFP := deqntAffineToF32(inputSeg[inPtrSeg+k*gridLen], zpSeg, scaleSeg)
							data.filterSegments = append(data.filterSegments, segElementFP)
						}

						data.objProbs = append(data.objProbs,
							deqntAffineToF32(maxClassProbs, zp, scale)*deqntAffineToF32(boxConfidence, zp, scale),
						)
						data.classID = append(data.classID, maxClassID)
						data.filterBoxes = append(data.filterBoxes, boxX, boxY, boxW, boxH)
						validCount++
					}
				}
			}
		}
	}

	return validCount
}

// initBufferPool initializes the buffer pool
func (y *YOLOv5Seg) initBufferPool(segData SegmentData,
	resizer *preprocess.Resizer) {

	if y.bufPoolInit {
		return
	}

	modelH := int(segData.data.height)
	modelW := int(segData.data.width)

	y.bufPool.Create(bufMatMul,
		y.Params.MaxObjectNumber*y.Params.PrototypeHeight*y.Params.PrototypeWeight)

	y.bufPool.Create(bufSegMask,
		y.Params.MaxObjectNumber*int(segData.data.height*segData.data.width))

	y.bufPool.Create(bufAllMask,
		int(segData.data.height*segData.data.width))

	croppedH := modelH - resizer.YPad()*2
	croppedW := modelW - resizer.XPad()*2

	y.bufPool.Create(bufCrop, croppedH*croppedW)

	y.bufPoolInit = true
}
