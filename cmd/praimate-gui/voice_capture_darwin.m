#import <AVFoundation/AVFoundation.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <math.h>

static _Atomic(uint64_t) praimate_voice_expected = 0;
static AVAudioEngine *praimate_voice_engine;
static NSMutableData *praimate_voice_data;
static NSLock *praimate_voice_control;
static NSLock *praimate_voice_data_lock;
static double praimate_voice_rate;
static uint64_t praimate_voice_running;

static void praimate_voice_locks(void) {
    static dispatch_once_t once;
    dispatch_once(&once, ^{ praimate_voice_control = [[NSLock alloc] init]; praimate_voice_data_lock = [[NSLock alloc] init]; });
}
static void praimate_voice_stop_locked(void) {
    if (praimate_voice_engine) {
        [praimate_voice_engine stop];
        [[praimate_voice_engine inputNode] removeTapOnBus:0];
        [praimate_voice_engine release];
        praimate_voice_engine = nil;
    }
    [praimate_voice_data_lock lock];
    [praimate_voice_data release]; praimate_voice_data = nil;
    [praimate_voice_data_lock unlock];
    praimate_voice_running = 0;
}
void praimate_voice_reserve(uint64_t session) { atomic_store(&praimate_voice_expected, session); }
void praimate_voice_cancel(void) {
    atomic_store(&praimate_voice_expected, 0); praimate_voice_locks();
    [praimate_voice_control lock]; praimate_voice_stop_locked(); [praimate_voice_control unlock];
}
int praimate_voice_start(uint64_t session) {
    @autoreleasepool {
        praimate_voice_locks();
        AVAuthorizationStatus status = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
        if (status == AVAuthorizationStatusNotDetermined) {
            dispatch_semaphore_t ready = dispatch_semaphore_create(0);
            [AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio completionHandler:^(BOOL granted) { dispatch_semaphore_signal(ready); }];
            if (dispatch_semaphore_wait(ready, dispatch_time(DISPATCH_TIME_NOW, 60LL * NSEC_PER_SEC)) != 0) return 0;
            status = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
        }
        if (status != AVAuthorizationStatusAuthorized || atomic_load(&praimate_voice_expected) != session) return 0;
        [praimate_voice_control lock];
        if (atomic_load(&praimate_voice_expected) != session) { [praimate_voice_control unlock]; return 0; }
        praimate_voice_stop_locked();
        AVAudioEngine *engine = [[AVAudioEngine alloc] init];
        AVAudioInputNode *input = [engine inputNode];
        AVAudioFormat *format = [input outputFormatForBus:0];
        double rate = [format sampleRate];
        if ([format channelCount] < 1 || [format isInterleaved] || [format commonFormat] != AVAudioPCMFormatFloat32 || rate < 8000 || rate > 192000) {
            [engine release]; [praimate_voice_control unlock]; return 0;
        }
        praimate_voice_rate = rate; praimate_voice_data = [[NSMutableData alloc] init];
        [input installTapOnBus:0 bufferSize:1024 format:format block:^(AVAudioPCMBuffer *buffer, AVAudioTime *when) {
            if (!buffer.floatChannelData || atomic_load(&praimate_voice_expected) != session) return;
            [praimate_voice_data_lock lock];
            NSUInteger cap = (NSUInteger)(rate * 120) * sizeof(float);
            NSUInteger used = [praimate_voice_data length];
            if (praimate_voice_data && used < cap) {
                NSUInteger bytes = MIN((NSUInteger)buffer.frameLength * sizeof(float), cap - used);
                [praimate_voice_data appendBytes:buffer.floatChannelData[0] length:bytes];
            }
            [praimate_voice_data_lock unlock];
        }];
        NSError *error = nil;
        if (![engine startAndReturnError:&error]) {
            [engine stop];
            [input removeTapOnBus:0]; [engine release];
            [praimate_voice_data_lock lock];
            [praimate_voice_data release]; praimate_voice_data = nil;
            [praimate_voice_data_lock unlock];
            [praimate_voice_control unlock]; return 0;
        }
        praimate_voice_engine = engine; praimate_voice_running = session;
        [praimate_voice_control unlock]; return 1;
    }
}
static void praimate_voice_u32(unsigned char *p, uint32_t value) { for (int i=0;i<4;i++) p[i]=(value>>(i*8))&255; }
static void praimate_voice_u16(unsigned char *p, uint16_t value) { p[0]=value&255;p[1]=(value>>8)&255; }
void *praimate_voice_finish(uint64_t session, int *length) {
    @autoreleasepool {
        *length=0; praimate_voice_locks(); [praimate_voice_control lock];
        if (!praimate_voice_engine || praimate_voice_running!=session || atomic_load(&praimate_voice_expected)!=session) { [praimate_voice_control unlock]; return NULL; }
        [praimate_voice_engine stop];
        [praimate_voice_data_lock lock]; NSData *data = [praimate_voice_data copy]; [praimate_voice_data_lock unlock];
        double rate=praimate_voice_rate; praimate_voice_stop_locked(); [praimate_voice_control unlock];
        const float *samples=[data bytes]; NSUInteger sampleCount=[data length]/sizeof(float);
        NSUInteger count=MIN((NSUInteger)(sampleCount*16000.0/rate),(NSUInteger)1920000);
        if (!count) { [data release]; return NULL; }
        unsigned char *wav=calloc(44+count*2,1);if (!wav) { [data release]; return NULL; }
        memcpy(wav,"RIFF",4);praimate_voice_u32(wav+4,(uint32_t)(36+count*2));memcpy(wav+8,"WAVEfmt ",8);
        praimate_voice_u32(wav+16,16);praimate_voice_u16(wav+20,1);praimate_voice_u16(wav+22,1);
        praimate_voice_u32(wav+24,16000);praimate_voice_u32(wav+28,32000);praimate_voice_u16(wav+32,2);praimate_voice_u16(wav+34,16);
        memcpy(wav+36,"data",4);praimate_voice_u32(wav+40,(uint32_t)(count*2));
        for (NSUInteger i=0;i<count;i++) {
            NSUInteger from=(NSUInteger)(i*rate/16000.0),to=MAX(from+1,(NSUInteger)((i+1)*rate/16000.0));
            double sum=0;for (NSUInteger j=from;j<to && j<sampleCount;j++) sum+=samples[j];
            double value=fmax(-1,fmin(1,sum/(to-from)));int16_t pcm=(int16_t)lrint(value*(value<0?32768:32767));praimate_voice_u16(wav+44+i*2,(uint16_t)pcm);
        }
        [data release];*length=(int)(44+count*2);return wav;
    }
}
